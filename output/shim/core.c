/* The pure half of libbdout.so: shim.conf, the clock table and the AOI maths.
 * No libdrm, no files, no globals, so test/core_test.c runs it on any host. */
#define _POSIX_C_SOURCE 200809L /* strtok_r under -std=c11 on glibc */
#include "bdout.h"

#include <errno.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static const uint32_t clocks_khz[] = {BDOUT_CLOCKS_KHZ};

static int fail(char *err, size_t errlen, const char *fmt, ...)
{
	if (err && errlen) {
		va_list ap;
		va_start(ap, fmt);
		vsnprintf(err, errlen, fmt, ap);
		va_end(ap);
	}
	return -1;
}

int bdout_clock_supported(uint32_t khz)
{
	for (size_t i = 0; i < sizeof clocks_khz / sizeof clocks_khz[0]; i++)
		if (clocks_khz[i] == khz)
			return 1;
	return 0;
}

uint32_t bdout_vrefresh(const bdout_drm_mode *m)
{
	uint64_t px = (uint64_t)m->htotal * m->vtotal;
	if (!px)
		return 0;
	return (uint32_t)(((uint64_t)m->clock * 1000 + px / 2) / px);
}

int bdout_mode_check(const bdout_drm_mode *m, char *err, size_t errlen)
{
	if (!bdout_clock_supported(m->clock))
		return fail(err, errlen, "pixel clock %u kHz is not one the PLAY's HDMI PHY can make", m->clock);
	if (!(m->hdisplay > 0 && m->hdisplay < m->hsync_start && m->hsync_start < m->hsync_end &&
	      m->hsync_end <= m->htotal))
		return fail(err, errlen, "horizontal timings out of order");
	if (!(m->vdisplay > 0 && m->vdisplay < m->vsync_start && m->vsync_start < m->vsync_end &&
	      m->vsync_end <= m->vtotal))
		return fail(err, errlen, "vertical timings out of order");
	if (m->hdisplay > BDOUT_MAX_WIDTH || m->vdisplay > BDOUT_MAX_WIDTH)
		return fail(err, errlen, "%ux%u is bigger than the PLAY can drive", m->hdisplay, m->vdisplay);
	if ((m->hdisplay | m->vdisplay) & 1)
		return fail(err, errlen, "width and height must be even");
	uint32_t sync = BDOUT_DRM_FLAG_PHSYNC | BDOUT_DRM_FLAG_NHSYNC | BDOUT_DRM_FLAG_PVSYNC | BDOUT_DRM_FLAG_NVSYNC;
	if (m->flags & ~sync)
		return fail(err, errlen, "only sync polarity flags are allowed (no interlace)");
	uint32_t hz = bdout_vrefresh(m);
	if (hz < 20 || hz > 120)
		return fail(err, errlen, "refresh %u Hz is outside 20-120", hz);
	return 0;
}

int bdout_fourcc_is_yuv(uint32_t fourcc)
{
	/* NV12/NV21/NV16/NV61/NV24/NV42/NV15 are all 'N','V',x,y; Rockchip's
	 * 10-bit NV12 is 'N','A','1','2'. PPApp's video is NV12, its OSD ARGB. */
	uint32_t lo = fourcc & 0xffff;
	return lo == ('N' | 'V' << 8) || lo == ('N' | 'A' << 8);
}

/* ---------------------------------------------------------------- shim.conf
 *
 *   version 1
 *   mode <name> <clock kHz> <hdisplay> <hsync_start> <hsync_end> <htotal>
 *        <vdisplay> <vsync_start> <vsync_end> <vtotal> <flags>
 *   aoi <sx> <sy> <sw> <sh> <dx> <dy> <dw> <dh> <fit>
 *
 * One line each, '#' starts a comment. Rectangles are millionths; fit is 0
 * (stretch) or 1 (keep the aspect ratio). Anything unexpected fails the whole
 * file, and a failed file means pass-through. */

static int parse_u32s(char *s, uint32_t *out, int n, char *err, size_t errlen, const char *what)
{
	char *save = NULL;
	for (int i = 0; i < n; i++) {
		char *tok = strtok_r(i ? NULL : s, " \t", &save);
		if (!tok)
			return fail(err, errlen, "%s: expected %d numbers, got %d", what, n, i);
		char *end;
		errno = 0;
		unsigned long v = strtoul(tok, &end, 10);
		if (errno || *end || tok[0] == '-' || v > 0xffffffffUL)
			return fail(err, errlen, "%s: '%s' is not a number", what, tok);
		out[i] = (uint32_t)v;
	}
	if (strtok_r(NULL, " \t", &save))
		return fail(err, errlen, "%s: more than %d numbers", what, n);
	return 0;
}

static int parse_mode(char *rest, bdout_drm_mode *m, char *err, size_t errlen)
{
	char *save = NULL;
	char *name = strtok_r(rest, " \t", &save);
	if (!name || strlen(name) >= BDOUT_DRM_DISPLAY_MODE_LEN)
		return fail(err, errlen, "mode: missing or overlong name");
	char *nums = strtok_r(NULL, "", &save);
	if (!nums)
		return fail(err, errlen, "mode: no timings");
	uint32_t v[10];
	if (parse_u32s(nums, v, 10, err, errlen, "mode"))
		return -1;
	for (int i = 1; i < 9; i++)
		if (v[i] > 0xffff)
			return fail(err, errlen, "mode: timing %u out of range", v[i]);
	memset(m, 0, sizeof *m);
	m->clock = v[0];
	m->hdisplay = (uint16_t)v[1];
	m->hsync_start = (uint16_t)v[2];
	m->hsync_end = (uint16_t)v[3];
	m->htotal = (uint16_t)v[4];
	m->vdisplay = (uint16_t)v[5];
	m->vsync_start = (uint16_t)v[6];
	m->vsync_end = (uint16_t)v[7];
	m->vtotal = (uint16_t)v[8];
	m->flags = v[9];
	m->vrefresh = bdout_vrefresh(m);
	m->type = BDOUT_DRM_MODE_TYPE_PREFERRED | BDOUT_DRM_MODE_TYPE_DRIVER;
	memcpy(m->name, name, strlen(name) + 1);
	return bdout_mode_check(m, err, errlen);
}

static int rect_ok(const bdout_rect *r)
{
	return r->w > 0 && r->h > 0 && r->x <= BDOUT_UNIT && r->y <= BDOUT_UNIT &&
	       r->w <= BDOUT_UNIT - r->x && r->h <= BDOUT_UNIT - r->y;
}

static int parse_aoi(char *rest, bdout_conf *c, char *err, size_t errlen)
{
	uint32_t v[9];
	if (parse_u32s(rest, v, 9, err, errlen, "aoi"))
		return -1;
	c->src = (bdout_rect){v[0], v[1], v[2], v[3]};
	c->dst = (bdout_rect){v[4], v[5], v[6], v[7]};
	if (!rect_ok(&c->src))
		return fail(err, errlen, "aoi: the source area is empty or runs off the picture");
	if (!rect_ok(&c->dst))
		return fail(err, errlen, "aoi: the output area is empty or runs off the screen");
	if (v[8] > BDOUT_FIT_ASPECT)
		return fail(err, errlen, "aoi: fit must be 0 or 1");
	c->fit = (int)v[8];
	return 0;
}

int bdout_parse_conf(const char *text, bdout_conf *out, char *err, size_t errlen)
{
	bdout_conf c;
	memset(&c, 0, sizeof c);
	memset(out, 0, sizeof *out);
	if (err && errlen)
		err[0] = 0;

	size_t n = strlen(text);
	char *buf = malloc(n + 1);
	if (!buf)
		return fail(err, errlen, "out of memory");
	memcpy(buf, text, n + 1);

	int version = 0, rc = 0, lineno = 0;
	char *save = NULL;
	for (char *line = strtok_r(buf, "\n", &save); line; line = strtok_r(NULL, "\n", &save)) {
		lineno++;
		char *hash = strchr(line, '#');
		if (hash)
			*hash = 0;
		size_t l = strlen(line);
		while (l && (line[l - 1] == ' ' || line[l - 1] == '\t' || line[l - 1] == '\r'))
			line[--l] = 0;
		while (*line == ' ' || *line == '\t')
			line++;
		if (!*line)
			continue;
		char *rest = line + strcspn(line, " \t");
		if (*rest)
			*rest++ = 0;
		while (*rest == ' ' || *rest == '\t')
			rest++;

		if (!strcmp(line, "version")) {
			if (strcmp(rest, "1")) {
				rc = fail(err, errlen, "line %d: version '%s', this shim reads version 1", lineno, rest);
				break;
			}
			version = 1;
		} else if (!strcmp(line, "mode")) {
			if (c.has_mode) {
				rc = fail(err, errlen, "line %d: a second mode", lineno);
				break;
			}
			if ((rc = parse_mode(rest, &c.mode, err, errlen)))
				break;
			c.has_mode = 1;
		} else if (!strcmp(line, "aoi")) {
			if (c.has_aoi) {
				rc = fail(err, errlen, "line %d: a second aoi", lineno);
				break;
			}
			if ((rc = parse_aoi(rest, &c, err, errlen)))
				break;
			c.has_aoi = 1;
		} else {
			rc = fail(err, errlen, "line %d: unknown setting '%s'", lineno, line);
			break;
		}
	}
	free(buf);
	if (rc)
		return -1;
	if (!version && (c.has_mode || c.has_aoi))
		return fail(err, errlen, "no 'version 1' line");
	*out = c;
	return 0;
}

/* ------------------------------------------------------------------- AOI */

/* Nearest, not floor: a crop typed in pixels reaches here as millionths, and
 * rounding gives the same pixel back for any picture up to 4096 wide (a third
 * of 1920 is 333333, which floors to 639). */
static uint32_t part(uint32_t n, uint32_t millionths)
{
	return (uint32_t)(((uint64_t)n * millionths + BDOUT_UNIT / 2) / BDOUT_UNIT);
}

static uint32_t even(uint32_t v) { return v & ~1u; }

int bdout_apply_aoi(const bdout_conf *c, const bdout_plane *in, bdout_plane *out,
		    char *err, size_t errlen)
{
	if (!c->has_aoi)
		return fail(err, errlen, "no AOI set");
	uint32_t bx = in->src_x >> 16, by = in->src_y >> 16;
	uint32_t pw = in->src_w >> 16, ph = in->src_h >> 16;
	uint32_t sw_ = in->crtc_w, sh_ = in->crtc_h;
	if (pw < 16 || ph < 16 || sw_ < 16 || sh_ < 16)
		return fail(err, errlen, "picture %ux%u or screen %ux%u too small", pw, ph, sw_, sh_);

	/* NV12 halves the chroma both ways, so the source rectangle starts and
	 * ends on even pixels; the destination is kept even to match. */
	uint32_t sx = even(part(pw, c->src.x)), sy = even(part(ph, c->src.y));
	uint32_t sw = even(part(pw, c->src.w)), sh = even(part(ph, c->src.h));
	if (sx + sw > pw)
		sw = even(pw - sx);
	if (sy + sh > ph)
		sh = even(ph - sy);

	uint32_t dx = even(part(sw_, c->dst.x)), dy = even(part(sh_, c->dst.y));
	uint32_t dw = even(part(sw_, c->dst.w)), dh = even(part(sh_, c->dst.h));
	if (dx + dw > sw_)
		dw = even(sw_ - dx);
	if (dy + dh > sh_)
		dh = even(sh_ - dy);

	if (sw < 16 || sh < 16)
		return fail(err, errlen, "the source area is %ux%u of a %ux%u picture, too small to show", sw, sh, pw, ph);
	if (dw < 16 || dh < 16)
		return fail(err, errlen, "the output area is %ux%u, too small to show", dw, dh);

	if (c->fit == BDOUT_FIT_ASPECT) {
		uint64_t a = (uint64_t)sw * dh, b = (uint64_t)dw * sh;
		if (a > b) { /* the source is wider: full width, bars above and below */
			uint32_t nh = even((uint32_t)(((uint64_t)dw * sh + sw / 2) / sw));
			dy += even((dh - nh) / 2);
			dh = nh;
		} else if (a < b) { /* taller: full height, bars at the sides */
			uint32_t nw = even((uint32_t)(((uint64_t)dh * sw + sh / 2) / sh));
			dx += even((dw - nw) / 2);
			dw = nw;
		}
		if (dw < 16 || dh < 16)
			return fail(err, errlen, "keeping the aspect ratio leaves a %ux%u picture", dw, dh);
	}

	if (sw > dw * BDOUT_MAX_SCALE || dw > sw * BDOUT_MAX_SCALE ||
	    sh > dh * BDOUT_MAX_SCALE || dh > sh * BDOUT_MAX_SCALE)
		return fail(err, errlen, "%ux%u to %ux%u is beyond the scaler's 8:1", sw, sh, dw, dh);

	out->crtc_x = in->crtc_x + (int32_t)dx;
	out->crtc_y = in->crtc_y + (int32_t)dy;
	out->crtc_w = dw;
	out->crtc_h = dh;
	out->src_x = (bx + sx) << 16;
	out->src_y = (by + sy) << 16;
	out->src_w = sw << 16;
	out->src_h = sh << 16;
	return 0;
}
