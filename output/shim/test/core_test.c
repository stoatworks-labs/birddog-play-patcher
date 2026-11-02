/* Host tests for core.c: shim.conf, the clock table and the AOI maths.
 *   cc -std=c11 -Wall -Wextra -Werror -I.. ../core.c core_test.c -o core_test && ./core_test
 * build.sh runs it; shim/test/run-linux.sh covers the wrappers. */
#include "../bdout.h"

#include <stdio.h>
#include <string.h>

static int failures, checks;

#define CHECK(cond, ...)                                                         \
	do {                                                                     \
		checks++;                                                        \
		if (!(cond)) {                                                   \
			failures++;                                              \
			printf("FAIL %s:%d: ", __FILE__, __LINE__);              \
			printf(__VA_ARGS__);                                     \
			printf("\n");                                            \
		}                                                                \
	} while (0)

#define MODE_1080P50 "mode 1920x1080 148500 1920 2448 2492 2640 1080 1084 1089 1125 5\n"

static void test_clocks(void)
{
	CHECK(bdout_clock_supported(148500), "148.5 MHz is in the table");
	CHECK(bdout_clock_supported(148352), "148.352 MHz (59.94) is in the table");
	CHECK(bdout_clock_supported(594000), "594 MHz is in the table");
	CHECK(!bdout_clock_supported(154000), "154 MHz (1920x1200 CVT-RB) is not");
	CHECK(!bdout_clock_supported(25175), "25.175 MHz (VGA) is not");
}

static void test_parse(void)
{
	bdout_conf c;
	char err[200];

	CHECK(bdout_parse_conf("", &c, err, sizeof err) == 0 && !c.has_mode && !c.has_aoi, "empty is all off");
	CHECK(bdout_parse_conf("# nothing\n\n  \n", &c, err, sizeof err) == 0 && !c.has_mode, "comments only");
	CHECK(bdout_parse_conf("version 1\n", &c, err, sizeof err) == 0 && !c.has_mode, "version alone");

	CHECK(bdout_parse_conf("version 1\n" MODE_1080P50, &c, err, sizeof err) == 0, "1080p50: %s", err);
	CHECK(c.has_mode && c.mode.clock == 148500 && c.mode.htotal == 2640 && c.mode.vtotal == 1125,
	      "1080p50 timings read");
	CHECK(c.mode.vrefresh == 50, "1080p50 refresh %u", c.mode.vrefresh);
	CHECK(!strcmp(c.mode.name, "1920x1080"), "name '%s'", c.mode.name);
	CHECK(c.mode.flags == (BDOUT_DRM_FLAG_PHSYNC | BDOUT_DRM_FLAG_PVSYNC), "flags %u", c.mode.flags);
	CHECK(c.mode.type & BDOUT_DRM_MODE_TYPE_PREFERRED, "the forced mode is preferred");

	CHECK(bdout_parse_conf("version 1 # trailing comment\r\n" MODE_1080P50, &c, err, sizeof err) == 0,
	      "CRLF and comments: %s", err);

	/* 1920x1200@60 on the 162 MHz clock, the one bdout's calculator picks. */
	CHECK(bdout_parse_conf("version 1\nmode 1920x1200 162000 1920 1968 2000 2080 1200 1203 1209 1298 9\n",
			       &c, err, sizeof err) == 0 && c.mode.vrefresh == 60,
	      "1920x1200@60 on 162 MHz: %s", err);

	CHECK(bdout_parse_conf("version 1\nmode 1920x1200 154000 1920 1968 2000 2080 1200 1203 1209 1235 9\n",
			       &c, err, sizeof err) == -1 && strstr(err, "154000"),
	      "a clock the PHY cannot make is refused: %s", err);
	CHECK(!c.has_mode && !c.has_aoi, "a refused file leaves everything off");

	CHECK(bdout_parse_conf(MODE_1080P50, &c, err, sizeof err) == -1, "a mode needs the version line");
	CHECK(bdout_parse_conf("version 2\n" MODE_1080P50, &c, err, sizeof err) == -1, "version 2 refused");
	CHECK(bdout_parse_conf("version 1\nwibble 3\n", &c, err, sizeof err) == -1 && strstr(err, "wibble"),
	      "unknown keys refused: %s", err);
	CHECK(bdout_parse_conf("version 1\n" MODE_1080P50 MODE_1080P50, &c, err, sizeof err) == -1,
	      "two modes refused");
	CHECK(bdout_parse_conf("version 1\nmode 1080i 74250 1920 2008 2052 2200 1080 1084 1094 1125 21\n",
			       &c, err, sizeof err) == -1,
	      "interlace refused");
	CHECK(bdout_parse_conf("version 1\nmode odd 148500 1919 2448 2492 2640 1080 1084 1089 1125 5\n",
			       &c, err, sizeof err) == -1,
	      "odd width refused");
	CHECK(bdout_parse_conf("version 1\nmode x 148500 1920 2448 2492 2640 1080 1084 1089\n", &c, err,
			       sizeof err) == -1,
	      "short mode refused");
	CHECK(bdout_parse_conf("version 1\nmode x 148500 1920 2448 2492 2640 1080 1084 1089 1125 5 9\n", &c,
			       err, sizeof err) == -1,
	      "long mode refused");
	CHECK(bdout_parse_conf("version 1\nmode x 148500 1920 2448 2492 2640 1080 1084 1089 -1125 5\n", &c,
			       err, sizeof err) == -1,
	      "negative refused");
	CHECK(bdout_parse_conf("version 1\nmode x 148500 1920 1900 2492 2640 1080 1084 1089 1125 5\n", &c,
			       err, sizeof err) == -1,
	      "sync inside the picture refused");
	CHECK(bdout_parse_conf("version 1\nmode 4096x2160x 594000 4097 4184 4272 4400 2160 2168 2178 2250 5\n",
			       &c, err, sizeof err) == -1,
	      "wider than the VOP refused");

	CHECK(bdout_parse_conf("version 1\naoi 500000 0 500000 1000000 0 0 1000000 1000000 0\n", &c, err,
			       sizeof err) == 0 && c.has_aoi && c.src.x == 500000 && c.fit == 0,
	      "aoi: %s", err);
	CHECK(bdout_parse_conf("version 1\naoi 600000 0 500000 1000000 0 0 1000000 1000000 0\n", &c, err,
			       sizeof err) == -1,
	      "an area off the edge of the picture is refused");
	CHECK(bdout_parse_conf("version 1\naoi 0 0 0 1000000 0 0 1000000 1000000 0\n", &c, err, sizeof err) == -1,
	      "an empty area is refused");
	CHECK(bdout_parse_conf("version 1\naoi 0 0 1000000 1000000 0 0 1000000 1000000 2\n", &c, err,
			       sizeof err) == -1,
	      "fit 2 refused");
}

static bdout_conf aoi(uint32_t sx, uint32_t sy, uint32_t sw, uint32_t sh, uint32_t dx, uint32_t dy,
		      uint32_t dw, uint32_t dh, int fit)
{
	bdout_conf c;
	memset(&c, 0, sizeof c);
	c.has_aoi = 1;
	c.src = (bdout_rect){sx, sy, sw, sh};
	c.dst = (bdout_rect){dx, dy, dw, dh};
	c.fit = fit;
	return c;
}

/* What PPApp sends for one frame: the whole picture on the whole screen. */
static bdout_plane frame(uint32_t pw, uint32_t ph, uint32_t sw, uint32_t sh)
{
	return (bdout_plane){0, 0, sw, sh, 0, 0, pw << 16, ph << 16};
}

#define ONE BDOUT_UNIT
#define HALF (BDOUT_UNIT / 2)

static void expect(const char *what, const bdout_conf *c, bdout_plane in, uint32_t sx, uint32_t sy, uint32_t sw,
		   uint32_t sh, int32_t dx, int32_t dy, uint32_t dw, uint32_t dh)
{
	bdout_plane out;
	char err[200] = "";
	int rc = bdout_apply_aoi(c, &in, &out, err, sizeof err);
	CHECK(rc == 0, "%s: refused: %s", what, err);
	if (rc)
		return;
	CHECK(out.src_x == sx << 16 && out.src_y == sy << 16 && out.src_w == sw << 16 && out.src_h == sh << 16,
	      "%s: source %ux%u at %u,%u, want %ux%u at %u,%u", what, out.src_w >> 16, out.src_h >> 16,
	      out.src_x >> 16, out.src_y >> 16, sw, sh, sx, sy);
	CHECK(out.crtc_x == dx && out.crtc_y == dy && out.crtc_w == dw && out.crtc_h == dh,
	      "%s: output %ux%u at %d,%d, want %ux%u at %d,%d", what, out.crtc_w, out.crtc_h, out.crtc_x,
	      out.crtc_y, dw, dh, dx, dy);
}

static void test_aoi(void)
{
	bdout_conf c;

	c = aoi(0, 0, ONE, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("whole picture", &c, frame(1920, 1080, 1920, 1080), 0, 0, 1920, 1080, 0, 0, 1920, 1080);

	c = aoi(0, 0, HALF, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("left half of a 3840x1080 wall", &c, frame(3840, 1080, 1920, 1080), 0, 0, 1920, 1080, 0, 0, 1920,
	       1080);
	c = aoi(HALF, 0, HALF, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("right half", &c, frame(3840, 1080, 1920, 1080), 1920, 0, 1920, 1080, 0, 0, 1920, 1080);

	c = aoi(HALF, HALF, HALF, HALF, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("bottom-right quadrant of 1080p, scaled up", &c, frame(1920, 1080, 1920, 1080), 960, 540, 960, 540,
	       0, 0, 1920, 1080);

	/* An LED wall canvas, 1536x864, sent 1:1 at the top left of 1080p. */
	c = aoi(0, 0, ONE, ONE, 0, 0, 800000, 800000, BDOUT_FIT_STRETCH);
	expect("1536x864 placed 1:1", &c, frame(1536, 864, 1920, 1080), 0, 0, 1536, 864, 0, 0, 1536, 864);

	c = aoi(0, 0, ONE, ONE, 0, 0, ONE, ONE, BDOUT_FIT_ASPECT);
	expect("16:9 on 16:10 letterboxed", &c, frame(1920, 1080, 1920, 1200), 0, 0, 1920, 1080, 0, 60, 1920,
	       1080);
	expect("4:3 on 16:9 pillarboxed", &c, frame(1440, 1080, 1920, 1080), 0, 0, 1440, 1080, 240, 0, 1440,
	       1080);
	expect("same shape, nothing to do", &c, frame(1280, 720, 1920, 1080), 0, 0, 1280, 720, 0, 0, 1920, 1080);

	/* Thirds of 1920 are 640, from 333333 as much as 333334; thirds of 1000
	 * are odd and come out even. */
	c = aoi(333333, 0, 333334, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("middle third", &c, frame(1920, 1080, 1920, 1080), 640, 0, 640, 1080, 0, 0, 1920, 1080);
	expect("odd sizes rounded even", &c, frame(1000, 600, 1920, 1080), 332, 0, 332, 600, 0, 0, 1920, 1080);
	c = aoi(666667, 0, 333333, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("last third", &c, frame(1920, 1080, 1920, 1080), 1280, 0, 640, 1080, 0, 0, 1920, 1080);

	/* A frame PPApp already offset is offset further, not reset. */
	bdout_plane in = frame(1920, 1080, 1920, 1080);
	in.src_x = 64 << 16;
	in.src_w = 1856 << 16;
	c = aoi(0, 0, HALF, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	expect("offset input", &c, in, 64, 0, 928, 1080, 0, 0, 1920, 1080);

	bdout_plane out, uhd = frame(3840, 2160, 1920, 1080), hd = frame(1920, 1080, 1920, 1080);
	char err[200];
	c = aoi(0, 0, 10000, 10000, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	CHECK(bdout_apply_aoi(&c, &uhd, &out, err, sizeof err) == -1 && strstr(err, "8:1"),
	      "a 38x20 corner blown up to the screen is past the scaler: %s", err);
	c = aoi(0, 0, 1000, 1000, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	CHECK(bdout_apply_aoi(&c, &hd, &out, err, sizeof err) == -1, "an area of 1 pixel is refused: %s", err);
	c = aoi(0, 0, ONE, ONE, 0, 0, ONE, ONE, BDOUT_FIT_STRETCH);
	c.has_aoi = 0;
	CHECK(bdout_apply_aoi(&c, &hd, &out, err, sizeof err) == -1, "no AOI, nothing applied");
}

static void test_fourcc(void)
{
	CHECK(bdout_fourcc_is_yuv(0x3231564e), "NV12 is video");
	CHECK(bdout_fourcc_is_yuv(0x3631564e), "NV16 is video");
	CHECK(bdout_fourcc_is_yuv(0x3231414e), "NA12 (Rockchip 10-bit) is video");
	CHECK(!bdout_fourcc_is_yuv(0x34325241), "AR24 (the OSD) is not");
	CHECK(!bdout_fourcc_is_yuv(0x34325258), "XR24 is not");
}

int main(void)
{
	test_clocks();
	test_parse();
	test_aoi();
	test_fourcc();
	printf("core: %d checks, %d failed\n", checks, failures);
	return failures ? 1 : 0;
}
