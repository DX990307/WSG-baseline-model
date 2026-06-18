#define _GNU_SOURCE

#include <errno.h>
#include <inttypes.h>
#include <sched.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/mman.h>
#include <unistd.h>
#include <x86intrin.h>

#define MAX_D_VALUES 256
#define MAX_K_VALUES 256

enum probe_mode {
	MODE_SWEEP,
	MODE_SAME_PTCL_D1,
	MODE_SAME_PTCL_D7,
	MODE_NEXT_PTCL_D8,
	MODE_FAR_RANDOM,
	MODE_HIT_ONLY,
	MODE_VPN8_SAME,
	MODE_VPN8_STRIDE8,
	MODE_VPN8_RANDOM,
	MODE_PTW_CAPACITY,
};

struct config {
	enum probe_mode mode;
	size_t trials;
	size_t perf_reps;
	size_t measure_pages;
	size_t evict_pages;
	size_t evict_steps;
	size_t cache_evict_bytes;
	size_t ptw_stride_pages;
	int max_d;
	int d_values[MAX_D_VALUES];
	int num_d_values;
	int max_k;
	int k_values[MAX_K_VALUES];
	int num_k_values;
	int cpu;
	int quiet;
	int perf_workload;
	int start_delay_ms;
	uint64_t seed;
};

struct mapping {
	uint8_t *raw;
	size_t raw_bytes;
	uint8_t *base;
	size_t pages;
};

struct probe_state {
	struct config cfg;
	size_t page_size;
	unsigned page_shift;
	struct mapping measure;
	struct mapping evict;
	struct mapping cache_evict;
	uint8_t *evict_head;
	size_t group_count;
	size_t *groups;
};

static volatile uint64_t global_sink;

static void usage(const char *prog)
{
	fprintf(stderr,
		"Usage: %s [options]\n"
		"\n"
		"Modes:\n"
		"  --mode sweep           timing sweep for d=1..max-d\n"
		"  --mode same_ptcl_d1    fixed d=1\n"
		"  --mode same_ptcl_d7    fixed d=7\n"
		"  --mode next_ptcl_d8    fixed d=8\n"
		"  --mode far_random      A and B are unrelated PTCL groups\n"
		"  --mode hit_only        TLB-hit lower-bound workload\n"
		"  --mode vpn8_same       access VPN+0..VPN+7 in one block\n"
		"  --mode vpn8_stride8    access 8 VPNs separated by 8 pages\n"
		"  --mode vpn8_random     access 8 unrelated VPNs in one block\n"
		"  --mode ptw_capacity    sweep independent TLB misses per batch\n"
		"\n"
		"Options:\n"
		"  --trials N             timing trials per d/mode (default 50000)\n"
		"  --perf-workload        run an interleaved A/B workload for perf stat\n"
		"  --perf-reps N          repetitions for --perf-workload (default 200)\n"
		"  --measure-pages N      pages in measurement mapping (default 65536)\n"
		"  --evict-pages N        pages in TLB eviction mapping (default 16384)\n"
		"  --evict-steps N        pointer-chase steps per eviction (default evict-pages)\n"
		"  --cache-evict-mb N     scan N MiB before timed misses to cold cachelines\n"
		"  --max-d N              maximum d for sweep (default 16)\n"
		"  --d-list LIST          comma-separated d values for sparse sweep\n"
		"  --max-k N              maximum K for ptw_capacity (default 128)\n"
		"  --k-list LIST          comma-separated K values for ptw_capacity\n"
		"  --ptw-stride-pages N   page distance between ptw_capacity loads (default 512)\n"
		"  --cpu N                pin process to CPU N\n"
		"  --seed N               shuffle seed (default 1)\n"
		"  --start-delay-ms N     sleep after setup so perf -D can skip setup\n"
		"  --quiet                suppress non-CSV chatter\n",
		prog);
}

static uint64_t parse_u64(const char *s, const char *name)
{
	char *end = NULL;
	errno = 0;
	uint64_t v = strtoull(s, &end, 0);
	if (errno != 0 || end == s || *end != '\0') {
		fprintf(stderr, "invalid %s: %s\n", name, s);
		exit(2);
	}
	return v;
}

static enum probe_mode parse_mode(const char *mode)
{
	if (strcmp(mode, "sweep") == 0) {
		return MODE_SWEEP;
	}
	if (strcmp(mode, "same_ptcl_d1") == 0) {
		return MODE_SAME_PTCL_D1;
	}
	if (strcmp(mode, "same_ptcl_d7") == 0) {
		return MODE_SAME_PTCL_D7;
	}
	if (strcmp(mode, "next_ptcl_d8") == 0) {
		return MODE_NEXT_PTCL_D8;
	}
	if (strcmp(mode, "far_random") == 0) {
		return MODE_FAR_RANDOM;
	}
	if (strcmp(mode, "hit_only") == 0) {
		return MODE_HIT_ONLY;
	}
	if (strcmp(mode, "vpn8_same") == 0) {
		return MODE_VPN8_SAME;
	}
	if (strcmp(mode, "vpn8_stride8") == 0) {
		return MODE_VPN8_STRIDE8;
	}
	if (strcmp(mode, "vpn8_random") == 0) {
		return MODE_VPN8_RANDOM;
	}
	if (strcmp(mode, "ptw_capacity") == 0) {
		return MODE_PTW_CAPACITY;
	}

	fprintf(stderr, "unknown mode: %s\n", mode);
	exit(2);
}

static const char *mode_name(enum probe_mode mode)
{
	switch (mode) {
	case MODE_SWEEP:
		return "sweep";
	case MODE_SAME_PTCL_D1:
		return "same_ptcl_d1";
	case MODE_SAME_PTCL_D7:
		return "same_ptcl_d7";
	case MODE_NEXT_PTCL_D8:
		return "next_ptcl_d8";
	case MODE_FAR_RANDOM:
		return "far_random";
	case MODE_HIT_ONLY:
		return "hit_only";
	case MODE_VPN8_SAME:
		return "vpn8_same";
	case MODE_VPN8_STRIDE8:
		return "vpn8_stride8";
	case MODE_VPN8_RANDOM:
		return "vpn8_random";
	case MODE_PTW_CAPACITY:
		return "ptw_capacity";
	}
	return "unknown";
}

static int mode_d(enum probe_mode mode)
{
	switch (mode) {
	case MODE_SAME_PTCL_D1:
		return 1;
	case MODE_SAME_PTCL_D7:
		return 7;
	case MODE_NEXT_PTCL_D8:
		return 8;
	default:
		return -1;
	}
}

static int is_vpn8_mode(enum probe_mode mode)
{
	return mode == MODE_VPN8_SAME || mode == MODE_VPN8_STRIDE8 ||
	       mode == MODE_VPN8_RANDOM;
}

static struct config default_config(void)
{
	struct config cfg;

	cfg.mode = MODE_SWEEP;
	cfg.trials = 50000;
	cfg.perf_reps = 200;
	cfg.measure_pages = 65536;
	cfg.evict_pages = 16384;
	cfg.evict_steps = 0;
	cfg.cache_evict_bytes = 0;
	cfg.ptw_stride_pages = 512;
	cfg.max_d = 16;
	cfg.num_d_values = 0;
	cfg.max_k = 128;
	cfg.num_k_values = 0;
	cfg.cpu = -1;
	cfg.quiet = 0;
	cfg.perf_workload = 0;
	cfg.start_delay_ms = 0;
	cfg.seed = 1;

	return cfg;
}

static void parse_d_list(struct config *cfg, const char *list)
{
	char *copy = strdup(list);
	char *token = NULL;
	char *saveptr = NULL;

	if (!copy) {
		perror("strdup d-list");
		exit(1);
	}

	for (token = strtok_r(copy, ",", &saveptr); token != NULL;
	     token = strtok_r(NULL, ",", &saveptr)) {
		if (cfg->num_d_values >= MAX_D_VALUES) {
			fprintf(stderr, "too many --d-list values; max=%d\n", MAX_D_VALUES);
			exit(2);
		}
		cfg->d_values[cfg->num_d_values++] = (int)parse_u64(token, "d-list");
		if (cfg->d_values[cfg->num_d_values - 1] < 1) {
			fprintf(stderr, "--d-list values must be positive\n");
			exit(2);
		}
	}

	free(copy);
}

static void parse_k_list(struct config *cfg, const char *list)
{
	char *copy = strdup(list);
	char *token = NULL;
	char *saveptr = NULL;

	if (!copy) {
		perror("strdup k-list");
		exit(1);
	}

	for (token = strtok_r(copy, ",", &saveptr); token != NULL;
	     token = strtok_r(NULL, ",", &saveptr)) {
		if (cfg->num_k_values >= MAX_K_VALUES) {
			fprintf(stderr, "too many --k-list values; max=%d\n", MAX_K_VALUES);
			exit(2);
		}
		cfg->k_values[cfg->num_k_values++] = (int)parse_u64(token, "k-list");
		if (cfg->k_values[cfg->num_k_values - 1] < 1) {
			fprintf(stderr, "--k-list values must be positive\n");
			exit(2);
		}
	}

	free(copy);
}

static int max_requested_d(const struct config *cfg)
{
	int max_d = cfg->max_d;
	for (int i = 0; i < cfg->num_d_values; i++) {
		if (cfg->d_values[i] > max_d) {
			max_d = cfg->d_values[i];
		}
	}
	return max_d;
}

static int max_requested_k(const struct config *cfg)
{
	int max_k = cfg->num_k_values > 0 ? cfg->k_values[0] : cfg->max_k;
	for (int i = 1; i < cfg->num_k_values; i++) {
		if (cfg->k_values[i] > max_k) {
			max_k = cfg->k_values[i];
		}
	}
	return max_k;
}

static struct config parse_args(int argc, char **argv)
{
	struct config cfg = default_config();

	for (int i = 1; i < argc; i++) {
		if (strcmp(argv[i], "--help") == 0 || strcmp(argv[i], "-h") == 0) {
			usage(argv[0]);
			exit(0);
		} else if (strcmp(argv[i], "--mode") == 0 && i + 1 < argc) {
			cfg.mode = parse_mode(argv[++i]);
		} else if (strcmp(argv[i], "--trials") == 0 && i + 1 < argc) {
			cfg.trials = (size_t)parse_u64(argv[++i], "trials");
		} else if (strcmp(argv[i], "--perf-workload") == 0) {
			cfg.perf_workload = 1;
		} else if (strcmp(argv[i], "--perf-reps") == 0 && i + 1 < argc) {
			cfg.perf_reps = (size_t)parse_u64(argv[++i], "perf-reps");
		} else if (strcmp(argv[i], "--measure-pages") == 0 && i + 1 < argc) {
			cfg.measure_pages = (size_t)parse_u64(argv[++i], "measure-pages");
		} else if (strcmp(argv[i], "--evict-pages") == 0 && i + 1 < argc) {
			cfg.evict_pages = (size_t)parse_u64(argv[++i], "evict-pages");
		} else if (strcmp(argv[i], "--evict-steps") == 0 && i + 1 < argc) {
			cfg.evict_steps = (size_t)parse_u64(argv[++i], "evict-steps");
		} else if (strcmp(argv[i], "--cache-evict-mb") == 0 && i + 1 < argc) {
			cfg.cache_evict_bytes =
				(size_t)parse_u64(argv[++i], "cache-evict-mb") << 20;
		} else if (strcmp(argv[i], "--max-d") == 0 && i + 1 < argc) {
			cfg.max_d = (int)parse_u64(argv[++i], "max-d");
		} else if (strcmp(argv[i], "--d-list") == 0 && i + 1 < argc) {
			parse_d_list(&cfg, argv[++i]);
		} else if (strcmp(argv[i], "--max-k") == 0 && i + 1 < argc) {
			cfg.max_k = (int)parse_u64(argv[++i], "max-k");
		} else if (strcmp(argv[i], "--k-list") == 0 && i + 1 < argc) {
			parse_k_list(&cfg, argv[++i]);
		} else if (strcmp(argv[i], "--ptw-stride-pages") == 0 && i + 1 < argc) {
			cfg.ptw_stride_pages =
				(size_t)parse_u64(argv[++i], "ptw-stride-pages");
		} else if (strcmp(argv[i], "--cpu") == 0 && i + 1 < argc) {
			cfg.cpu = (int)parse_u64(argv[++i], "cpu");
		} else if (strcmp(argv[i], "--seed") == 0 && i + 1 < argc) {
			cfg.seed = parse_u64(argv[++i], "seed");
		} else if (strcmp(argv[i], "--start-delay-ms") == 0 && i + 1 < argc) {
			cfg.start_delay_ms = (int)parse_u64(argv[++i], "start-delay-ms");
		} else if (strcmp(argv[i], "--quiet") == 0) {
			cfg.quiet = 1;
		} else {
			fprintf(stderr, "unknown or incomplete argument: %s\n", argv[i]);
			usage(argv[0]);
			exit(2);
		}
	}

	if (cfg.trials == 0 || cfg.perf_reps == 0 || cfg.measure_pages < 4096 ||
	    cfg.evict_pages < 1024 || cfg.max_d < 1 || cfg.max_k < 1 ||
	    cfg.ptw_stride_pages < 1) {
		fprintf(stderr, "invalid benchmark size; use --help for defaults\n");
		exit(2);
	}
	if (max_requested_k(&cfg) > MAX_K_VALUES) {
		fprintf(stderr, "ptw_capacity K must be <= %d\n", MAX_K_VALUES);
		exit(2);
	}
	if (cfg.evict_steps == 0) {
		cfg.evict_steps = cfg.evict_pages;
	}

	return cfg;
}

static void pin_cpu(int cpu)
{
	if (cpu < 0) {
		return;
	}

	cpu_set_t set;
	CPU_ZERO(&set);
	CPU_SET(cpu, &set);
	if (sched_setaffinity(0, sizeof(set), &set) != 0) {
		fprintf(stderr, "sched_setaffinity(%d) failed: %s\n", cpu, strerror(errno));
		exit(1);
	}
}

static unsigned log2_u64(size_t v)
{
	unsigned shift = 0;
	while ((1ULL << shift) < v) {
		shift++;
	}
	return shift;
}

static uint64_t rng_next(uint64_t *state)
{
	uint64_t x = *state;
	x ^= x << 13;
	x ^= x >> 7;
	x ^= x << 17;
	*state = x;
	return x;
}

static void *checked_mmap(size_t bytes)
{
	void *p = mmap(NULL, bytes, PROT_READ | PROT_WRITE,
		       MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (p == MAP_FAILED) {
		fprintf(stderr, "mmap(%zu) failed: %s\n", bytes, strerror(errno));
		exit(1);
	}

	if (madvise(p, bytes, MADV_NOHUGEPAGE) != 0) {
		fprintf(stderr, "warning: MADV_NOHUGEPAGE failed: %s\n", strerror(errno));
	}

	return p;
}

static struct mapping make_mapping(size_t pages, size_t page_size, unsigned page_shift)
{
	struct mapping m;
	size_t slack_pages = 16;
	uintptr_t addr;

	m.raw_bytes = (pages + slack_pages) * page_size;
	m.raw = checked_mmap(m.raw_bytes);

	addr = (uintptr_t)m.raw;
	while (((addr >> page_shift) & 7ULL) != 0) {
		addr += page_size;
	}

	m.base = (uint8_t *)addr;
	m.pages = pages;

	for (size_t i = 0; i < pages; i++) {
		m.base[i * page_size] = (uint8_t)i;
	}

	return m;
}

static struct mapping make_byte_mapping(size_t bytes)
{
	struct mapping m;

	memset(&m, 0, sizeof(m));
	if (bytes == 0) {
		return m;
	}

	m.raw_bytes = bytes;
	m.raw = checked_mmap(m.raw_bytes);
	m.base = m.raw;
	m.pages = bytes;

	for (size_t i = 0; i < bytes; i += 64) {
		m.base[i] = (uint8_t)i;
	}

	return m;
}

static void init_evict_chain(struct probe_state *st)
{
	size_t n = st->cfg.evict_pages;
	size_t *order = calloc(n, sizeof(*order));
	if (!order) {
		perror("calloc evict order");
		exit(1);
	}

	for (size_t i = 0; i < n; i++) {
		order[i] = i;
	}

	uint64_t seed = st->cfg.seed ^ 0x9e3779b97f4a7c15ULL;
	for (size_t i = n - 1; i > 0; i--) {
		size_t j = rng_next(&seed) % (i + 1);
		size_t tmp = order[i];
		order[i] = order[j];
		order[j] = tmp;
	}

	for (size_t i = 0; i < n; i++) {
		size_t cur = order[i];
		size_t next = order[(i + 1) % n];
		uint8_t **slot = (uint8_t **)(st->evict.base + cur * st->page_size);
		*slot = st->evict.base + next * st->page_size;
	}
	st->evict_head = st->evict.base + order[0] * st->page_size;
	free(order);
}

static void init_groups(struct probe_state *st)
{
	size_t far_offset = 1024;
	size_t max_offset = far_offset;
	int requested_d = max_requested_d(&st->cfg);
	int requested_k = max_requested_k(&st->cfg);
	if ((size_t)requested_d > max_offset) {
		max_offset = (size_t)requested_d;
	}
	if (st->cfg.mode == MODE_PTW_CAPACITY) {
		size_t ptw_offset = ((size_t)requested_k - 1) *
				    st->cfg.ptw_stride_pages;
		if (ptw_offset > max_offset) {
			max_offset = ptw_offset;
		}
	}
	if (st->cfg.measure_pages <= max_offset + 8) {
		fprintf(stderr,
			"not enough measurement pages for requested offsets; "
			"measure_pages=%zu required>%zu\n",
			st->cfg.measure_pages, max_offset + 8);
		exit(2);
	}

	size_t usable_pages = st->cfg.measure_pages - max_offset - 8;
	st->group_count = usable_pages / 8;
	if (st->group_count < 1024) {
		fprintf(stderr, "not enough PTCL groups; increase --measure-pages\n");
		exit(2);
	}

	st->groups = calloc(st->group_count, sizeof(*st->groups));
	if (!st->groups) {
		perror("calloc groups");
		exit(1);
	}

	for (size_t i = 0; i < st->group_count; i++) {
		st->groups[i] = i * 8;
	}

	uint64_t seed = st->cfg.seed;
	for (size_t i = st->group_count - 1; i > 0; i--) {
		size_t j = rng_next(&seed) % (i + 1);
		size_t tmp = st->groups[i];
		st->groups[i] = st->groups[j];
		st->groups[j] = tmp;
	}
}

static void init_state(struct probe_state *st, struct config cfg)
{
	memset(st, 0, sizeof(*st));
	st->cfg = cfg;
	st->page_size = (size_t)sysconf(_SC_PAGESIZE);
	st->page_shift = log2_u64(st->page_size);

	if (st->page_size != 4096) {
		fprintf(stderr, "expected 4KB pages, got %zu\n", st->page_size);
		exit(1);
	}

	st->measure = make_mapping(cfg.measure_pages, st->page_size, st->page_shift);
	st->evict = make_mapping(cfg.evict_pages, st->page_size, st->page_shift);
	st->cache_evict = make_byte_mapping(cfg.cache_evict_bytes);
	init_evict_chain(st);
	init_groups(st);
}

static inline uint64_t rdtsc_start(void)
{
	_mm_lfence();
	return __rdtsc();
}

static inline uint64_t rdtsc_stop(void)
{
	unsigned aux;
	uint64_t t = __rdtscp(&aux);
	_mm_lfence();
	return t;
}

static inline uint8_t load_byte(uint8_t *addr)
{
	return *(volatile uint8_t *)addr;
}

static inline uint64_t time_load(uint8_t *addr)
{
	uint64_t start = rdtsc_start();
	global_sink += load_byte(addr);
	return rdtsc_stop() - start;
}

static inline uint64_t time_batch_load(uint8_t **addrs, size_t k)
{
	uint64_t s0 = 0;
	uint64_t s1 = 0;
	uint64_t s2 = 0;
	uint64_t s3 = 0;
	uint64_t s4 = 0;
	uint64_t s5 = 0;
	uint64_t s6 = 0;
	uint64_t s7 = 0;
	size_t i = 0;

	__asm__ __volatile__("" ::: "memory");
	uint64_t start = rdtsc_start();

	for (; i + 7 < k; i += 8) {
		s0 += load_byte(addrs[i + 0]);
		s1 += load_byte(addrs[i + 1]);
		s2 += load_byte(addrs[i + 2]);
		s3 += load_byte(addrs[i + 3]);
		s4 += load_byte(addrs[i + 4]);
		s5 += load_byte(addrs[i + 5]);
		s6 += load_byte(addrs[i + 6]);
		s7 += load_byte(addrs[i + 7]);
	}
	for (; i < k; i++) {
		s0 += load_byte(addrs[i]);
	}

	uint64_t cycles = rdtsc_stop() - start;
	global_sink += s0 + s1 + s2 + s3 + s4 + s5 + s6 + s7;
	return cycles;
}

static void evict_tlb(struct probe_state *st)
{
	uint8_t *p = st->evict_head;
	for (size_t i = 0; i < st->cfg.evict_steps; i++) {
		p = *(uint8_t **)p;
	}
	global_sink += (uintptr_t)p;
}

static void evict_cache(struct probe_state *st)
{
	uint8_t *buf = st->cache_evict.base;
	size_t bytes = st->cache_evict.raw_bytes;

	if (buf == NULL || bytes == 0) {
		return;
	}

	for (size_t i = 0; i < bytes; i += 64) {
		global_sink += *(volatile uint8_t *)(buf + i);
	}
	_mm_mfence();
}

static void evict_for_timed_miss(struct probe_state *st)
{
	evict_cache(st);
	evict_tlb(st);
}

static int compare_u64(const void *a, const void *b)
{
	uint64_t aa = *(const uint64_t *)a;
	uint64_t bb = *(const uint64_t *)b;
	if (aa < bb) {
		return -1;
	}
	if (aa > bb) {
		return 1;
	}
	return 0;
}

static uint64_t percentile(uint64_t *values, size_t n, double pct)
{
	size_t idx;
	if (n == 0) {
		return 0;
	}
	idx = (size_t)((n - 1) * pct);
	return values[idx];
}

static size_t trial_group(struct probe_state *st, size_t trial)
{
	return st->groups[trial % st->group_count];
}

static uint8_t *page_addr(struct probe_state *st, size_t page)
{
	return st->measure.base + page * st->page_size;
}

static uint8_t *far_b_addr(struct probe_state *st, size_t trial, size_t a_group)
{
	size_t idx = (trial * 1103515245ULL + 12345ULL) % st->group_count;
	size_t b_group = st->groups[idx];
	if (b_group == a_group) {
		b_group = st->groups[(idx + 1) % st->group_count];
	}
	return page_addr(st, b_group);
}

static void vpn8_addrs(struct probe_state *st, enum probe_mode mode,
		       size_t trial, uint8_t *addrs[8])
{
	size_t group = trial_group(st, trial);

	switch (mode) {
	case MODE_VPN8_SAME:
		for (size_t i = 0; i < 8; i++) {
			addrs[i] = page_addr(st, group + i);
		}
		break;
	case MODE_VPN8_STRIDE8:
		for (size_t i = 0; i < 8; i++) {
			addrs[i] = page_addr(st, group + i * 8);
		}
		break;
	case MODE_VPN8_RANDOM:
		for (size_t i = 0; i < 8; i++) {
			size_t idx = (trial * 1103515245ULL + i * 2654435761ULL + 17ULL) %
				     st->group_count;
			addrs[i] = page_addr(st, st->groups[idx]);
		}
		break;
	default:
		for (size_t i = 0; i < 8; i++) {
			addrs[i] = page_addr(st, group + i);
		}
		break;
	}
}

static void run_one_timing(struct probe_state *st, enum probe_mode mode, int d)
{
	size_t n = st->cfg.trials;
	uint64_t *a = calloc(n, sizeof(*a));
	uint64_t *b1 = calloc(n, sizeof(*b1));
	uint64_t *b2 = calloc(n, sizeof(*b2));
	uint64_t *cold = calloc(n, sizeof(*cold));
	if (!a || !b1 || !b2 || !cold) {
		perror("calloc timing arrays");
		exit(1);
	}

	for (size_t t = 0; t < n; t++) {
		size_t group = trial_group(st, t);
		uint8_t *addr_a = page_addr(st, group);
		uint8_t *addr_b;

		if (mode == MODE_FAR_RANDOM) {
			addr_b = far_b_addr(st, t, group);
		} else if (mode == MODE_HIT_ONLY) {
			addr_b = page_addr(st, group + 1);
		} else {
			addr_b = page_addr(st, group + (size_t)d);
		}

		global_sink += load_byte(addr_a);
		global_sink += load_byte(addr_b);

		if (mode == MODE_HIT_ONLY) {
			global_sink += load_byte(addr_a);
			global_sink += load_byte(addr_b);
			a[t] = time_load(addr_a);
			b1[t] = time_load(addr_b);
			b2[t] = time_load(addr_b);
			cold[t] = time_load(addr_b);
			continue;
		}

		evict_for_timed_miss(st);
		a[t] = time_load(addr_a);
		b1[t] = time_load(addr_b);
		b2[t] = time_load(addr_b);

		evict_for_timed_miss(st);
		cold[t] = time_load(addr_b);
	}

	qsort(a, n, sizeof(*a), compare_u64);
	qsort(b1, n, sizeof(*b1), compare_u64);
	qsort(b2, n, sizeof(*b2), compare_u64);
	qsort(cold, n, sizeof(*cold), compare_u64);

	printf("%s,%d,%zu,%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64
	       ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64
	       ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n",
	       mode_name(mode), d, n,
	       percentile(a, n, 0.50), percentile(b1, n, 0.50),
	       percentile(b2, n, 0.50), percentile(cold, n, 0.50),
	       percentile(a, n, 0.05), percentile(b1, n, 0.05),
	       percentile(b2, n, 0.05), percentile(cold, n, 0.05),
	       percentile(a, n, 0.95), percentile(b1, n, 0.95),
	       percentile(b2, n, 0.95), percentile(cold, n, 0.95));

	free(a);
	free(b1);
	free(b2);
	free(cold);
}

static void run_timing(struct probe_state *st)
{
	if (st->cfg.mode == MODE_PTW_CAPACITY) {
		size_t n = st->cfg.trials;
		int default_k_values[] = {1, 2, 4, 8, 12, 16, 20,
					  24, 32, 48, 64, 96, 128};
		int num_default_k_values =
			(int)(sizeof(default_k_values) / sizeof(default_k_values[0]));
		int max_k = max_requested_k(&st->cfg);
		uint8_t **addrs = calloc((size_t)max_k, sizeof(*addrs));
		uint64_t *total = calloc(n, sizeof(*total));
		uint64_t *per_load = calloc(n, sizeof(*per_load));

		if (!addrs || !total || !per_load) {
			perror("calloc ptw capacity arrays");
			exit(1);
		}

		printf("mode,k,trials,stride_pages,total_median,cycles_per_load_median,"
		       "total_p05,total_p95,cycles_per_load_p05,cycles_per_load_p95\n");

		int count = st->cfg.num_k_values > 0 ? st->cfg.num_k_values :
							  num_default_k_values;
		for (int ki = 0; ki < count; ki++) {
			int k = st->cfg.num_k_values > 0 ? st->cfg.k_values[ki] :
							   default_k_values[ki];
			if (k > max_k) {
				continue;
			}

			for (size_t t = 0; t < n; t++) {
				size_t base = trial_group(st, t);
				for (int i = 0; i < k; i++) {
					addrs[i] = page_addr(
						st,
						base +
							(size_t)i *
								st->cfg.ptw_stride_pages);
					global_sink += load_byte(addrs[i]);
				}

				evict_for_timed_miss(st);
				total[t] = time_batch_load(addrs, (size_t)k);
				per_load[t] = total[t] / (uint64_t)k;
			}

			qsort(total, n, sizeof(*total), compare_u64);
			qsort(per_load, n, sizeof(*per_load), compare_u64);
			printf("%s,%d,%zu,%zu,%" PRIu64 ",%" PRIu64
			       ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n",
			       mode_name(st->cfg.mode), k, n,
			       st->cfg.ptw_stride_pages,
			       percentile(total, n, 0.50),
			       percentile(per_load, n, 0.50),
			       percentile(total, n, 0.05),
			       percentile(total, n, 0.95),
			       percentile(per_load, n, 0.05),
			       percentile(per_load, n, 0.95));
		}

		free(addrs);
		free(total);
		free(per_load);
		return;
	}

	if (is_vpn8_mode(st->cfg.mode)) {
		size_t n = st->cfg.trials;
		uint64_t *slots = calloc(n * 8, sizeof(*slots));
		if (!slots) {
			perror("calloc vpn8 timing arrays");
			exit(1);
		}

		for (size_t t = 0; t < n; t++) {
			uint8_t *addrs[8];
			vpn8_addrs(st, st->cfg.mode, t, addrs);

			for (size_t i = 0; i < 8; i++) {
				global_sink += load_byte(addrs[i]);
			}

			evict_for_timed_miss(st);
			for (size_t i = 0; i < 8; i++) {
				slots[i * n + t] = time_load(addrs[i]);
			}
		}

		printf("mode,slot,trials,median,p05,p95\n");
		for (size_t i = 0; i < 8; i++) {
			uint64_t *values = slots + i * n;
			qsort(values, n, sizeof(*values), compare_u64);
			printf("%s,%zu,%zu,%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n",
			       mode_name(st->cfg.mode), i, n,
			       percentile(values, n, 0.50),
			       percentile(values, n, 0.05),
			       percentile(values, n, 0.95));
		}

		free(slots);
		return;
	}

	printf("mode,d,trials,a_median,b1_after_a_median,b2_hit_median,cold_b_median,"
	       "a_p05,b1_after_a_p05,b2_hit_p05,cold_b_p05,"
	       "a_p95,b1_after_a_p95,b2_hit_p95,cold_b_p95\n");

	if (st->cfg.mode == MODE_SWEEP) {
		if (st->cfg.num_d_values > 0) {
			for (int i = 0; i < st->cfg.num_d_values; i++) {
				run_one_timing(st, MODE_SWEEP, st->cfg.d_values[i]);
			}
		} else {
			for (int d = 1; d <= st->cfg.max_d; d++) {
				run_one_timing(st, MODE_SWEEP, d);
			}
		}
		return;
	}

	run_one_timing(st, st->cfg.mode, mode_d(st->cfg.mode));
}

static uint8_t *perf_b_addr(struct probe_state *st, enum probe_mode mode,
			    size_t rep, size_t i, size_t group)
{
	switch (mode) {
	case MODE_SAME_PTCL_D1:
		return page_addr(st, group + 1);
	case MODE_SAME_PTCL_D7:
		return page_addr(st, group + 7);
	case MODE_NEXT_PTCL_D8:
		return page_addr(st, group + 8);
	case MODE_FAR_RANDOM: {
		size_t idx = (i * 1103515245ULL + rep * 12345ULL + 17ULL) % st->group_count;
		size_t b_group = st->groups[idx];
		if (b_group == group) {
			b_group = st->groups[(idx + 1) % st->group_count];
		}
		return page_addr(st, b_group);
	}
	case MODE_HIT_ONLY:
		return page_addr(st, group + 1);
	default:
		return page_addr(st, group + 1);
	}
}

static void run_perf_workload(struct probe_state *st)
{
	size_t active_groups = st->group_count;

	if (st->cfg.mode == MODE_SWEEP) {
		fprintf(stderr, "--perf-workload requires a fixed --mode, not sweep\n");
		exit(2);
	}

	if (st->cfg.mode == MODE_HIT_ONLY) {
		active_groups = active_groups < 64 ? active_groups : 64;
		for (size_t i = 0; i < active_groups; i++) {
			size_t group = st->groups[i];
			global_sink += load_byte(page_addr(st, group));
			global_sink += load_byte(page_addr(st, group + 1));
		}
	}

	for (size_t rep = 0; rep < st->cfg.perf_reps; rep++) {
		for (size_t i = 0; i < active_groups; i++) {
			size_t group = st->groups[i];
			uint8_t *addr_a = page_addr(st, group);
			uint8_t *addr_b = perf_b_addr(st, st->cfg.mode, rep, i, group);

			if (is_vpn8_mode(st->cfg.mode)) {
				uint8_t *addrs[8];
				vpn8_addrs(st, st->cfg.mode, rep * active_groups + i, addrs);
				for (size_t j = 0; j < 8; j++) {
					global_sink += load_byte(addrs[j]);
				}
			} else {
				global_sink += load_byte(addr_a);
				global_sink += load_byte(addr_b);
				global_sink += load_byte(addr_b);
			}
		}
	}

	if (!st->cfg.quiet) {
		printf("mode=%s,perf_reps=%zu,groups=%zu,sink=%" PRIu64 "\n",
		       mode_name(st->cfg.mode), st->cfg.perf_reps,
		       active_groups, global_sink);
	}
}

int main(int argc, char **argv)
{
	struct config cfg = parse_args(argc, argv);
	struct probe_state st;

	pin_cpu(cfg.cpu);
	init_state(&st, cfg);

	if (!cfg.quiet) {
		fprintf(stderr,
			"ptcl_probe: page_size=%zu measure_pages=%zu evict_pages=%zu "
			"cache_evict_mb=%zu groups=%zu mode=%s trials=%zu perf_reps=%zu\n",
			st.page_size, cfg.measure_pages, cfg.evict_pages,
			cfg.cache_evict_bytes >> 20, st.group_count,
			mode_name(cfg.mode), cfg.trials,
			cfg.perf_reps);
	}

	if (cfg.start_delay_ms > 0) {
		usleep((useconds_t)cfg.start_delay_ms * 1000U);
	}

	if (cfg.perf_workload) {
		run_perf_workload(&st);
	} else {
		run_timing(&st);
	}

	return 0;
}
