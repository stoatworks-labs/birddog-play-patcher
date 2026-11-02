/* libbdout.so: custom HDMI formats and areas of interest inside PPApp.
 *
 * Loaded into the PLAY's own decoder with LD_PRELOAD (a drop-in on
 * BirdDogRunner.service), it wraps four libdrm calls PPApp makes:
 *
 *   drmModeGetConnector  the HDMI connector's mode list becomes the one format
 *                        bdout chose, so PPApp's own matcher can only pick it
 *   drmModeAddFB2        remembers each framebuffer's pixel format
 *   drmModeRmFB          forgets it again
 *   drmModeSetPlane      a YUV frame placed at the origin is the video; its
 *                        source and destination rectangles become the AOI
 *
 * Everything else passes straight through, and so does everything when
 * shim.conf is missing, empty or unreadable. This header is the pure part,
 * with no libdrm and no I/O, so the host tests can exercise it.
 *
 * The structures below mirror libdrm's xf86drmMode.h (MIT), whose layouts are
 * part of libdrm's stable ABI. The PLAY has no libdrm headers to build
 * against, and the shim must not link libdrm: BirdDogRunner's shell script
 * loads it too.
 */
#ifndef BDOUT_H
#define BDOUT_H

#include <stddef.h>
#include <stdint.h>

#define BDOUT_DRM_DISPLAY_MODE_LEN 32

typedef struct {
	uint32_t clock; /* kHz */
	uint16_t hdisplay, hsync_start, hsync_end, htotal, hskew;
	uint16_t vdisplay, vsync_start, vsync_end, vtotal, vscan;
	uint32_t vrefresh;
	uint32_t flags;
	uint32_t type;
	char name[BDOUT_DRM_DISPLAY_MODE_LEN];
} bdout_drm_mode;

typedef struct {
	uint32_t connector_id;
	uint32_t encoder_id;
	int connection; /* drmModeConnection */
	uint32_t mm_width, mm_height;
	int subpixel; /* drmModeSubPixel */
	int count_modes;
	bdout_drm_mode *modes;
	int count_props;
	uint32_t *props;
	uint64_t *prop_values;
	int count_encoders;
	uint32_t *encoders;
	uint32_t connector_type;
	uint32_t connector_type_id;
} bdout_drm_connector;

#define BDOUT_DRM_CONNECTED 1
#define BDOUT_DRM_CONNECTOR_HDMIA 11
#define BDOUT_DRM_MODE_TYPE_PREFERRED (1u << 3)
#define BDOUT_DRM_MODE_TYPE_DRIVER (1u << 6)
#define BDOUT_DRM_FLAG_PHSYNC (1u << 0)
#define BDOUT_DRM_FLAG_NHSYNC (1u << 1)
#define BDOUT_DRM_FLAG_PVSYNC (1u << 2)
#define BDOUT_DRM_FLAG_NVSYNC (1u << 3)

/* The RK3328's HDMI PHY makes these pixel clocks and no others: its pre-PLL
 * table in the PLAY's 4.4.194 kernel, rows where the TMDS clock equals the
 * pixel clock. A mode on any other clock is pruned by the kernel when it comes
 * from an EDID, and when a program hands it over directly the VOP ends up with
 * a pixel clock of 0: a dark screen, not an error. So nothing leaves this shim
 * on a clock that is not in the table. output/timings.go carries the same list
 * and a Go test holds the two together. */
#define BDOUT_CLOCKS_KHZ                                                         \
	27000, 40000, 59341, 59400, 65000, 71000, 74176, 74250, 83500, 85750,    \
	    88750, 108000, 119000, 148352, 148500, 162000, 296703, 297000,       \
	    593407, 594000

/* The VOP drives at most 4096 pixels across. */
#define BDOUT_MAX_WIDTH 4096
/* Its scaler goes no further than 8:1 either way. */
#define BDOUT_MAX_SCALE 8

/* Rectangles in shim.conf are millionths of the picture or of the screen. */
#define BDOUT_UNIT 1000000u

typedef struct {
	uint32_t x, y, w, h;
} bdout_rect;

enum { BDOUT_FIT_STRETCH = 0, BDOUT_FIT_ASPECT = 1 };

typedef struct {
	int has_mode;
	bdout_drm_mode mode;
	int has_aoi;
	bdout_rect src; /* of the decoded picture */
	bdout_rect dst; /* of the screen */
	int fit;
} bdout_conf;

/* A drmModeSetPlane call's geometry. src is 16.16 fixed point, as libdrm has it. */
typedef struct {
	int32_t crtc_x, crtc_y;
	uint32_t crtc_w, crtc_h;
	uint32_t src_x, src_y, src_w, src_h;
} bdout_plane;

int bdout_clock_supported(uint32_t khz);
uint32_t bdout_vrefresh(const bdout_drm_mode *m);

/* 0 if the mode is one the PLAY can put out; otherwise -1 and why in err. */
int bdout_mode_check(const bdout_drm_mode *m, char *err, size_t errlen);

/* Parse shim.conf. Returns 0, or -1 with a reason in err; on -1 *out is all
 * off, so a broken file means "pass everything through". */
int bdout_parse_conf(const char *text, bdout_conf *out, char *err, size_t errlen);

/* Is this framebuffer pixel format one of the YUV ones PPApp decodes into? */
int bdout_fourcc_is_yuv(uint32_t fourcc);

/* Work out the AOI for one SetPlane call. Returns 0 and fills *out, or -1 with
 * a reason (the caller then passes the call through unchanged). */
int bdout_apply_aoi(const bdout_conf *c, const bdout_plane *in, bdout_plane *out,
		    char *err, size_t errlen);

#endif
