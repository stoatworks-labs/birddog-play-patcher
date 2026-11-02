/* core.c on the host, for bdout's Go tests: they write shim.conf files and ask
 * this whether the shim would read them the same way, and where it would put
 * the picture.
 *
 *   confcheck FILE                      "ok mode <name> <hz> | aoi <fit>" or "error: ..."
 *   confcheck FILE PWxPH SWxSH          also the plane the AOI gives for a
 *                                       PWxPH picture on a SWxSH screen:
 *                                       "plane sx sy sw sh dx dy dw dh"
 *
 * Exit 0 when the file parses (and the AOI applies), 1 otherwise. */
#include "../bdout.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int main(int argc, char **argv)
{
	if (argc != 2 && argc != 4) {
		fprintf(stderr, "usage: confcheck FILE [PWxPH SWxSH]\n");
		return 2;
	}
	FILE *f = fopen(argv[1], "r");
	if (!f) {
		perror(argv[1]);
		return 2;
	}
	static char text[16384];
	size_t n = fread(text, 1, sizeof text - 1, f);
	fclose(f);
	text[n] = 0;

	bdout_conf c;
	char err[200];
	if (bdout_parse_conf(text, &c, err, sizeof err)) {
		printf("error: %s\n", err);
		return 1;
	}
	printf("ok");
	if (c.has_mode)
		printf(" mode %s %u", c.mode.name, c.mode.vrefresh);
	if (c.has_aoi)
		printf(" aoi %d", c.fit);
	printf("\n");
	if (argc == 2)
		return 0;

	unsigned pw, ph, sw, sh;
	if (sscanf(argv[2], "%ux%u", &pw, &ph) != 2 || sscanf(argv[3], "%ux%u", &sw, &sh) != 2) {
		fprintf(stderr, "sizes are WxH\n");
		return 2;
	}
	bdout_plane in = {0, 0, sw, sh, 0, 0, pw << 16, ph << 16}, out;
	if (bdout_apply_aoi(&c, &in, &out, err, sizeof err)) {
		printf("error: %s\n", err);
		return 1;
	}
	printf("plane %u %u %u %u %d %d %u %u\n", out.src_x >> 16, out.src_y >> 16, out.src_w >> 16,
	       out.src_h >> 16, out.crtc_x, out.crtc_y, out.crtc_w, out.crtc_h);
	return 0;
}
