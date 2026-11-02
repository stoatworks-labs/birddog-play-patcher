/* Drives libbdout.so the way PPApp does, against fakedrm.c's libdrm.so.2.
 * run-linux.sh builds both and runs this with the shim preloaded; it needs
 * BDOUT_CONF and BDOUT_STATUS pointing somewhere writable. */
#include "../bdout.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

typedef struct {
	uint32_t plane, fb;
	int32_t crtc_x, crtc_y;
	uint32_t crtc_w, crtc_h, src_x, src_y, src_w, src_h;
} fake_call;

/* libdrm, as PPApp sees it (the fake, with the shim in front). */
bdout_drm_connector *drmModeGetConnector(int fd, uint32_t id);
void drmModeFreeConnector(bdout_drm_connector *c);
int drmModeAddFB2(int fd, uint32_t width, uint32_t height, uint32_t pixel_format, const uint32_t bo_handles[4],
		  const uint32_t pitches[4], const uint32_t offsets[4], uint32_t *buf_id, uint32_t flags);
int drmModeRmFB(int fd, uint32_t id);
int drmModeSetPlane(int fd, uint32_t plane, uint32_t crtc, uint32_t fb, uint32_t flags, int32_t crtc_x,
		    int32_t crtc_y, uint32_t crtc_w, uint32_t crtc_h, uint32_t src_x, uint32_t src_y,
		    uint32_t src_w, uint32_t src_h);

/* The fake's record of what reached "the kernel". */
extern fake_call fake_last;
extern int fake_set_plane_calls;
extern int fake_refuse_aoi;
extern int fake_connected;

#define NV12 0x3231564eu
#define AR24 0x34325241u

static int failures, checks;
static const char *conf_path, *status_path;

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

/* Write shim.conf (NULL removes it) and wait out the shim's half-second
 * between looks. */
static void set_conf(const char *text)
{
	if (!text) {
		unlink(conf_path);
	} else {
		char tmp[512];
		snprintf(tmp, sizeof tmp, "%s.tmp", conf_path);
		FILE *f = fopen(tmp, "w");
		fputs(text, f);
		fclose(f);
		rename(tmp, conf_path);
	}
	usleep(600 * 1000);
}

static char status[2048];

static const char *read_status(void)
{
	FILE *f = fopen(status_path, "r");
	status[0] = 0;
	if (f) {
		size_t n = fread(status, 1, sizeof status - 1, f);
		status[n] = 0;
		fclose(f);
	}
	return status;
}

static uint32_t add_fb(uint32_t fourcc)
{
	uint32_t z[4] = {0}, id = 0;
	drmModeAddFB2(3, 1920, 1080, fourcc, z, z, z, &id, 0);
	return id;
}

/* One video frame as PPApp shows it: the whole picture on the whole screen. */
static int frame(uint32_t fb, uint32_t pw, uint32_t ph)
{
	return drmModeSetPlane(3, 31, 40, fb, 0, 0, 0, 1920, 1080, 0, 0, pw << 16, ph << 16);
}

static int got(int32_t cx, int32_t cy, uint32_t cw, uint32_t ch, uint32_t sx, uint32_t sy, uint32_t sw,
	       uint32_t sh)
{
	fake_call l = fake_last;
	int ok = l.crtc_x == cx && l.crtc_y == cy && l.crtc_w == cw && l.crtc_h == ch && l.src_x == sx << 16 &&
		 l.src_y == sy << 16 && l.src_w == sw << 16 && l.src_h == sh << 16;
	if (!ok)
		printf("     kernel got %ux%u at %d,%d from %ux%u at %u,%u\n", l.crtc_w, l.crtc_h, l.crtc_x, l.crtc_y,
		       l.src_w >> 16, l.src_h >> 16, l.src_x >> 16, l.src_y >> 16);
	return ok;
}

int main(void)
{
	conf_path = getenv("BDOUT_CONF");
	status_path = getenv("BDOUT_STATUS");
	if (!conf_path || !status_path) {
		fprintf(stderr, "set BDOUT_CONF and BDOUT_STATUS\n");
		return 2;
	}

	/* No shim.conf: everything as it was. */
	set_conf(NULL);
	bdout_drm_connector *c = drmModeGetConnector(3, 47);
	CHECK(c && c->count_modes == 3, "no conf: the screen's three modes, got %d", c ? c->count_modes : -1);
	drmModeFreeConnector(c);
	CHECK(strstr(read_status(), "conf=missing"), "status says missing:\n%s", status);
	uint32_t video = add_fb(NV12), osd = add_fb(AR24);
	CHECK(frame(video, 1920, 1080) == 0 && got(0, 0, 1920, 1080, 0, 0, 1920, 1080), "no conf: frame untouched");
	CHECK(strstr(read_status(), "picture=1920x1080 on 1920x1080"), "status shows the picture with AOI off:\n%s", status);
	frame(osd, 1280, 720);
	CHECK(strstr(read_status(), "picture=1920x1080 on 1920x1080"), "the OSD is not the picture:\n%s", status);

	/* A forced format: the mode list is that one mode. */
	set_conf("version 1\nmode 1920x1200 162000 1920 1968 2000 2080 1200 1203 1209 1298 9\n");
	c = drmModeGetConnector(3, 47);
	CHECK(c && c->count_modes == 1, "forced: one mode, got %d", c ? c->count_modes : -1);
	if (c && c->count_modes == 1) {
		bdout_drm_mode *m = &c->modes[0];
		CHECK(m->hdisplay == 1920 && m->vdisplay == 1200 && m->clock == 162000 && m->vrefresh == 60,
		      "forced: %ux%u %u kHz %u Hz", m->hdisplay, m->vdisplay, m->clock, m->vrefresh);
		CHECK(m->type & BDOUT_DRM_MODE_TYPE_PREFERRED, "forced mode marked preferred");
		CHECK(!strcmp(m->name, "1920x1200"), "forced mode name %s", m->name);
	}
	drmModeFreeConnector(c); /* libdrm frees the shim's list with free() */
	CHECK(strstr(read_status(), "mode=forced 1920x1200 at 60 Hz"), "status says forced:\n%s", status);

	fake_connected = 2;
	c = drmModeGetConnector(3, 47);
	CHECK(c && c->count_modes == 3, "no screen: list left alone, got %d", c ? c->count_modes : -1);
	drmModeFreeConnector(c);
	CHECK(strstr(read_status(), "no screen connected"), "status says waiting:\n%s", status);
	fake_connected = 1;
	c = drmModeGetConnector(3, 99);
	CHECK(c && c->count_modes == 3, "not HDMI: left alone, got %d", c ? c->count_modes : -1);
	drmModeFreeConnector(c);
	c = drmModeGetConnector(3, 47);
	CHECK(c && c->count_modes == 1, "screen back: forced again, got %d", c ? c->count_modes : -1);
	drmModeFreeConnector(c);

	/* An AOI: the right half of a 3840x1080 canvas. */
	set_conf("version 1\naoi 500000 0 500000 1000000 0 0 1000000 1000000 0\n");
	CHECK(frame(video, 3840, 1080) == 0 && got(0, 0, 1920, 1080, 1920, 0, 1920, 1080), "AOI: right half");
	/* The format is gone from shim.conf, but PPApp is still on the one it
	 * was handed until it asks again, and the status must say so. */
	CHECK(strstr(read_status(), "mode=forced 1920x1200"), "status still says what PPApp has:\n%s", status);
	CHECK(strstr(read_status(), "aoi=on") && strstr(status, "1920x1080 at 1920,0 of a 3840x1080 picture"),
	      "status shows the geometry:\n%s", status);
	CHECK(strstr(status, "picture=3840x1080 on 1920x1080"), "status shows the new picture:\n%s", status);
	CHECK(frame(osd, 3840, 1080) == 0 && got(0, 0, 1920, 1080, 0, 0, 3840, 1080), "AOI: ARGB (the OSD) untouched");
	CHECK(drmModeSetPlane(3, 31, 40, video, 0, 100, 50, 640, 360, 0, 0, 640 << 16, 360 << 16) == 0 &&
		  got(100, 50, 640, 360, 0, 0, 640, 360),
	      "AOI: a frame placed off the origin untouched");
	CHECK(frame(9999, 3840, 1080) == 0 && got(0, 0, 1920, 1080, 0, 0, 3840, 1080), "AOI: unknown fb untouched");
	drmModeRmFB(3, video);
	CHECK(frame(video, 3840, 1080) == 0 && got(0, 0, 1920, 1080, 0, 0, 3840, 1080), "AOI: removed fb untouched");
	c = drmModeGetConnector(3, 47);
	CHECK(c && c->count_modes == 3, "AOI without a format: the screen's modes, got %d", c ? c->count_modes : -1);
	drmModeFreeConnector(c);
	CHECK(strstr(read_status(), "mode=off: the screen's own modes"), "status says off once PPApp asks:\n%s", status);

	/* The kernel refuses the AOI: PPApp's own call goes through instead, and
	 * the shim stops trying rather than doubling every frame. */
	video = add_fb(NV12);
	set_conf("version 1\naoi 0 0 1000000 1000000 0 0 500000 500000 0\n");
	fake_refuse_aoi = 1;
	int before = fake_set_plane_calls;
	CHECK(frame(video, 1920, 1080) == 0, "refused: PPApp still sees success");
	CHECK(fake_set_plane_calls - before == 2, "refused: tried, then PPApp's call (%d calls)", fake_set_plane_calls - before);
	CHECK(got(0, 0, 1920, 1080, 0, 0, 1920, 1080), "refused: the frame shown as PPApp asked");
	CHECK(strstr(read_status(), "refused by the display"), "status says refused:\n%s", status);
	before = fake_set_plane_calls;
	frame(video, 1920, 1080);
	CHECK(fake_set_plane_calls - before == 1, "refused: no second try (%d calls)", fake_set_plane_calls - before);
	fake_refuse_aoi = 0;

	/* A broken file: everything passes through. */
	set_conf("version 1\nmode 1920x1200 154000 1920 1968 2000 2080 1200 1203 1209 1235 9\naoi 0 0 500000 500000 0 0 1000000 1000000 0\n");
	c = drmModeGetConnector(3, 47);
	CHECK(c && c->count_modes == 3, "broken conf: the screen's modes, got %d", c ? c->count_modes : -1);
	drmModeFreeConnector(c);
	CHECK(frame(video, 1920, 1080) == 0 && got(0, 0, 1920, 1080, 0, 0, 1920, 1080), "broken conf: frame untouched");
	CHECK(strstr(read_status(), "conf=error: pixel clock 154000 kHz"), "status names the error:\n%s", status);

	printf("shim: %d checks, %d failed\n", checks, failures);
	return failures ? 1 : 0;
}
