// nv12check — is VMX_DecodeNV12 exactly the stock UYVY decode, repacked?
//
//   nv12check FILE.vmxs [THREADS]
//
// For every frame of a recorded OMT stream: decode to UYVY with the stock entry point,
// decode again to NV12 with the patch's, and require Y to match byte for byte and each
// CbCr sample to be the rounded mean of the two UYVY rows it stands for. Then times both
// decodes over the whole file. Exits 1 on the first mismatch.
//
// .vmxs is what the OMT spike's omtrec wrote: seven uint32s ('VMXS', version, width,
// height, fps, quality, frame count), then per frame a uint32 length and the VMX data
// exactly as it arrived on the wire.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "vmxcodec.h"

static double now(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return ts.tv_sec + ts.tv_nsec / 1e9;
}

int main(int argc, char** argv) {
  if (argc < 2) {
    fprintf(stderr, "usage: nv12check FILE.vmxs [THREADS]\n");
    return 2;
  }
  FILE* f = fopen(argv[1], "rb");
  if (!f) { perror(argv[1]); return 2; }
  uint32_t hdr[7];
  if (fread(hdr, sizeof hdr, 1, f) != 1 || hdr[0] != 0x53584D56u) {
    fprintf(stderr, "%s: not a .vmxs\n", argv[1]);
    return 2;
  }
  int w = (int)hdr[2], h = (int)hdr[3], n = (int)hdr[6];
  uint8_t** data = calloc(n, sizeof *data);
  uint32_t* len = calloc(n, sizeof *len);
  for (int i = 0; i < n; i++) {
    if (fread(&len[i], 4, 1, f) != 1) { fprintf(stderr, "short file\n"); return 2; }
    data[i] = malloc(len[i]);
    if (fread(data[i], 1, len[i], f) != len[i]) { fprintf(stderr, "short file\n"); return 2; }
  }
  fclose(f);

  VMX_SIZE sz = {w, h};
  VMX_INSTANCE* v = VMX_Create(sz, VMX_PROFILE_OMT_SQ, VMX_COLORSPACE_BT709);
  if (argc > 2) VMX_SetThreads(v, atoi(argv[2]));

  uint8_t* uyvy = malloc((size_t)w * 2 * h);
  uint8_t* nv12 = malloc((size_t)w * h * 3 / 2 + w);
  uint8_t* uv = nv12 + (size_t)w * h;
  int ch = (h + 1) / 2;

  for (int i = 0; i < n; i++) {
    if (VMX_LoadFrom(v, data[i], (int)len[i]) != VMX_ERR_OK) { fprintf(stderr, "frame %d: LoadFrom failed\n", i); return 1; }
    if (VMX_DecodeUYVY(v, uyvy, w * 2) != VMX_ERR_OK) { fprintf(stderr, "frame %d: DecodeUYVY failed\n", i); return 1; }
    memset(nv12, 0xA5, (size_t)w * h * 3 / 2);
    if (VMX_LoadFrom(v, data[i], (int)len[i]) != VMX_ERR_OK) return 1;
    if (VMX_DecodeNV12(v, nv12, w, uv, w) != VMX_ERR_OK) { fprintf(stderr, "frame %d: DecodeNV12 failed\n", i); return 1; }
    for (int y = 0; y < h; y++)
      for (int x = 0; x < w; x++)
        if (nv12[(size_t)y * w + x] != uyvy[(size_t)y * w * 2 + x * 2 + 1]) {
          fprintf(stderr, "frame %d: Y differs at %d,%d\n", i, x, y);
          return 1;
        }
    for (int r = 0; r < ch; r++) {
      const uint8_t* a = uyvy + (size_t)(2 * r) * w * 2;
      const uint8_t* b = (2 * r + 1 < h) ? a + (size_t)w * 2 : a;
      for (int x = 0; x < w / 2; x++) {
        int cb = (a[x * 4] + b[x * 4] + 1) >> 1;
        int cr = (a[x * 4 + 2] + b[x * 4 + 2] + 1) >> 1;
        const uint8_t* d = uv + (size_t)r * w + x * 2;
        if (d[0] != cb || d[1] != cr) {
          fprintf(stderr, "frame %d: CbCr differs at %d,%d (got %d,%d want %d,%d)\n", i, x, r, d[0], d[1], cb, cr);
          return 1;
        }
      }
    }
  }

  double t0 = now();
  for (int i = 0; i < n; i++) { VMX_LoadFrom(v, data[i], (int)len[i]); VMX_DecodeUYVY(v, uyvy, w * 2); }
  double t1 = now();
  for (int i = 0; i < n; i++) { VMX_LoadFrom(v, data[i], (int)len[i]); VMX_DecodeNV12(v, nv12, w, uv, w); }
  double t2 = now();
  printf("%s: %dx%d, %d frames OK on %d threads; ms/frame uyvy %.2f nv12 %.2f\n", argv[1], w, h, n,
         VMX_GetThreads(v), (t1 - t0) * 1000 / n, (t2 - t1) * 1000 / n);
  VMX_Destroy(v);
  return 0;
}
