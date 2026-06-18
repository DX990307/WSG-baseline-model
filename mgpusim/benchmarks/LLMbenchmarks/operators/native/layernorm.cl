#include "common.h"

__kernel void llm_layernorm(__global float *out,
                            const __global float *in,
                            int rows,
                            int hidden,
                            float epsilon) {
  int gid = get_global_id(0);
  int n = rows * hidden;
  if (gid >= n) {
    return;
  }

  int row = gid / hidden;
  int col = gid - row * hidden;
  int base = row * hidden;
  int peer_col = col + (hidden >> 1);
  if (peer_col >= hidden) {
    peer_col -= hidden;
  }

  float x = in[gid];
  float peer = in[base + peer_col];
  float center = llm_synthetic_value(row, col, hidden, 17) * 0.03125F;
  float variance = fabs(peer) * 0.015625F + 1.0F;
  float inv_std = llm_safe_rsqrt(variance, epsilon);
  out[gid] = (x - center) * inv_std;
}
