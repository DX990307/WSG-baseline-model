CC ?= gcc
CFLAGS ?= -O3 -march=native -Wall -Wextra -std=c11
LDFLAGS ?=

.PHONY: all clean

all: ptcl_probe

ptcl_probe: ptcl_probe.c
	$(CC) $(CFLAGS) -o $@ $< $(LDFLAGS)

clean:
	rm -f ptcl_probe
