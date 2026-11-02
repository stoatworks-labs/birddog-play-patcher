/* A stand-in libdrm.so.2 for testing libbdout.so off a PLAY: the five calls
 * the shim touches, each recording what reached "the kernel". The harness
 * links against this, the shim is preloaded in front of it, and whatever the
 * shim did shows up in fake_last. */
#include "../bdout.h"

#include <stdlib.h>
#include <string.h>

typedef struct {
	uint32_t plane, fb;
	int32_t crtc_x, crtc_y;
	uint32_t crtc_w, crtc_h, src_x, src_y, src_w, src_h;
} fake_call;

fake_call fake_last;
int fake_set_plane_calls;
/* When set, any SetPlane that is not the whole picture on the whole screen
 * fails, as a VOP refusing an AOI would. */
int fake_refuse_aoi;
int fake_connected = BDOUT_DRM_CONNECTED;
static uint32_t next_fb = 100;

static bdout_drm_mode std_mode(const char *name, uint32_t clock, uint16_t h, uint16_t hss, uint16_t hse,
			       uint16_t ht, uint16_t v, uint16_t vss, uint16_t vse, uint16_t vt, uint32_t hz)
{
	bdout_drm_mode m;
	memset(&m, 0, sizeof m);
	m.clock = clock;
	m.hdisplay = h, m.hsync_start = hss, m.hsync_end = hse, m.htotal = ht;
	m.vdisplay = v, m.vsync_start = vss, m.vsync_end = vse, m.vtotal = vt;
	m.vrefresh = hz;
	m.flags = BDOUT_DRM_FLAG_PHSYNC | BDOUT_DRM_FLAG_PVSYNC;
	m.type = BDOUT_DRM_MODE_TYPE_DRIVER;
	strcpy(m.name, name);
	return m;
}

bdout_drm_connector *drmModeGetConnector(int fd, uint32_t id)
{
	(void)fd;
	bdout_drm_connector *c = calloc(1, sizeof *c);
	c->connector_id = id;
	c->connection = fake_connected;
	c->connector_type = id == 99 ? 1 /* VGA */ : BDOUT_DRM_CONNECTOR_HDMIA;
	c->count_modes = 3;
	c->modes = calloc(3, sizeof *c->modes);
	c->modes[0] = std_mode("1920x1080", 148500, 1920, 2008, 2052, 2200, 1080, 1084, 1089, 1125, 60);
	c->modes[0].type |= BDOUT_DRM_MODE_TYPE_PREFERRED;
	c->modes[1] = std_mode("1280x720", 74250, 1280, 1390, 1430, 1650, 720, 725, 730, 750, 60);
	c->modes[2] = std_mode("3840x2160", 297000, 3840, 4016, 4104, 4400, 2160, 2168, 2178, 2250, 30);
	return c;
}

void drmModeFreeConnector(bdout_drm_connector *c)
{
	if (!c)
		return;
	free(c->modes);
	free(c);
}

int drmModeAddFB2(int fd, uint32_t width, uint32_t height, uint32_t pixel_format, const uint32_t bo_handles[4],
		  const uint32_t pitches[4], const uint32_t offsets[4], uint32_t *buf_id, uint32_t flags)
{
	(void)fd, (void)width, (void)height, (void)pixel_format, (void)bo_handles, (void)pitches, (void)offsets,
	    (void)flags;
	*buf_id = next_fb++;
	return 0;
}

int drmModeRmFB(int fd, uint32_t id)
{
	(void)fd, (void)id;
	return 0;
}

int drmModeSetPlane(int fd, uint32_t plane, uint32_t crtc, uint32_t fb, uint32_t flags, int32_t crtc_x,
		    int32_t crtc_y, uint32_t crtc_w, uint32_t crtc_h, uint32_t src_x, uint32_t src_y,
		    uint32_t src_w, uint32_t src_h)
{
	(void)fd, (void)crtc, (void)flags;
	fake_set_plane_calls++;
	fake_last = (fake_call){plane, fb, crtc_x, crtc_y, crtc_w, crtc_h, src_x, src_y, src_w, src_h};
	if (fake_refuse_aoi && (crtc_x || crtc_y || src_x || src_y || crtc_w != 1920 || crtc_h != 1080))
		return -22;
	return 0;
}
