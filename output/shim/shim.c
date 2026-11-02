/* The libdrm wrappers of libbdout.so. See bdout.h for what each one does.
 *
 * Rules this file keeps:
 *   - Never link libdrm. BirdDogRunner's shell script and every program it
 *     runs get LD_PRELOAD too, so the real functions are looked up with
 *     dlsym(RTLD_NEXT) when PPApp first calls them, and nothing else notices.
 *   - When in doubt, pass through. A missing or broken shim.conf, a framebuffer
 *     we did not see created, a SetPlane the kernel refuses: PPApp gets exactly
 *     the call it made.
 *   - Nothing per frame touches the disk. shim.conf is looked at no more than
 *     twice a second, and the status file is only written when what it says
 *     changes.
 */
#define _GNU_SOURCE
#include "bdout.h"

#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#define EXPORT __attribute__((visibility("default")))

#define DEFAULT_CONF "/userdata/bd-out/shim.conf"
#define DEFAULT_STATUS "/run/bdout-shim.status"
#define RECHECK_NS 500000000LL
#define FB_SLOTS 256
#define CONF_MAX 16384

typedef bdout_drm_connector *(*get_connector_fn)(int, uint32_t);
typedef int (*add_fb2_fn)(int, uint32_t, uint32_t, uint32_t, const uint32_t[4], const uint32_t[4],
			  const uint32_t[4], uint32_t *, uint32_t);
typedef int (*rm_fb_fn)(int, uint32_t);
typedef int (*set_plane_fn)(int, uint32_t, uint32_t, uint32_t, uint32_t, int32_t, int32_t, uint32_t,
			    uint32_t, uint32_t, uint32_t, uint32_t, uint32_t);

static pthread_once_t once = PTHREAD_ONCE_INIT;
static get_connector_fn real_get_connector;
static add_fb2_fn real_add_fb2;
static rm_fb_fn real_rm_fb;
static set_plane_fn real_set_plane;
static const char *conf_path = DEFAULT_CONF;
static const char *status_path = DEFAULT_STATUS;

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static bdout_conf conf;           /* what is in force */
static char conf_state[240];      /* "ok", "missing" or the parse error */
static int checked;
static struct timespec last_check;
static struct stat conf_st;
static int conf_exists;

static struct {
	uint32_t id, fourcc;
} fbs[FB_SLOTS];
static unsigned fb_next;

static int aoi_failed;            /* latched until shim.conf changes */
static char aoi_state[240];
static char aoi_geom[160];
static char mode_state[160];     /* the list PPApp was last handed: what it is using */
static char picture[64];          /* the last video frame: "<picture> on <screen>" */

static void init(void)
{
	real_get_connector = (get_connector_fn)dlsym(RTLD_NEXT, "drmModeGetConnector");
	real_add_fb2 = (add_fb2_fn)dlsym(RTLD_NEXT, "drmModeAddFB2");
	real_rm_fb = (rm_fb_fn)dlsym(RTLD_NEXT, "drmModeRmFB");
	real_set_plane = (set_plane_fn)dlsym(RTLD_NEXT, "drmModeSetPlane");
	const char *c = getenv("BDOUT_CONF"), *s = getenv("BDOUT_STATUS");
	if (c && *c)
		conf_path = c;
	if (s && *s)
		status_path = s;
	snprintf(mode_state, sizeof mode_state, "not asked yet");
	snprintf(aoi_state, sizeof aoi_state, "off");
}

/* key=value lines, replaced whole by rename so bdout never reads half. Values
 * are ours and carry no newlines. */
static void write_status_locked(void)
{
	char tmp[512], buf[1024];
	snprintf(tmp, sizeof tmp, "%s.%d", status_path, (int)getpid());
	int n = snprintf(buf, sizeof buf,
			 "pid=%d\nconf=%s\nmode=%s\naoi=%s\naoi_geometry=%s\npicture=%s\nupdated=%ld\n",
			 (int)getpid(), conf_state, mode_state, aoi_state, aoi_geom, picture, (long)time(NULL));
	int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
	if (fd < 0)
		return;
	ssize_t w = write(fd, buf, (size_t)n);
	close(fd);
	if (w != n || rename(tmp, status_path))
		unlink(tmp);
}

static int same_file(const struct stat *a, const struct stat *b)
{
	return a->st_dev == b->st_dev && a->st_ino == b->st_ino && a->st_size == b->st_size &&
	       a->st_mtim.tv_sec == b->st_mtim.tv_sec && a->st_mtim.tv_nsec == b->st_mtim.tv_nsec;
}

static void load_locked(void)
{
	char err[200];
	bdout_conf next;
	memset(&next, 0, sizeof next);

	int fd = open(conf_path, O_RDONLY | O_CLOEXEC);
	if (fd < 0) {
		snprintf(conf_state, sizeof conf_state, "missing (%s): passing everything through", conf_path);
	} else {
		char *buf = malloc(CONF_MAX + 1);
		ssize_t got = buf ? read(fd, buf, CONF_MAX) : -1;
		close(fd);
		if (got < 0) {
			snprintf(conf_state, sizeof conf_state, "unreadable: passing everything through");
		} else if (got == CONF_MAX) {
			snprintf(conf_state, sizeof conf_state, "too long: passing everything through");
		} else {
			buf[got] = 0;
			if (bdout_parse_conf(buf, &next, err, sizeof err))
				snprintf(conf_state, sizeof conf_state, "error: %s: passing everything through", err);
			else
				snprintf(conf_state, sizeof conf_state, "ok");
		}
		free(buf);
	}
	conf = next;
	aoi_failed = 0;
	aoi_geom[0] = 0;
	if (conf.has_aoi)
		snprintf(aoi_state, sizeof aoi_state, "on: waiting for a video frame");
	else
		snprintf(aoi_state, sizeof aoi_state, "off");
	fprintf(stderr, "bdout: shim.conf %s; format %s, AOI %s\n", conf_state,
		conf.has_mode ? conf.mode.name : "follows the source", conf.has_aoi ? "on" : "off");
	write_status_locked();
}

/* Look at shim.conf again if half a second has passed, and reload it if it
 * changed. Called with mu held. */
static void refresh_locked(void)
{
	struct timespec now;
	clock_gettime(CLOCK_MONOTONIC, &now);
	if (checked) {
		long long ns = (now.tv_sec - last_check.tv_sec) * 1000000000LL + (now.tv_nsec - last_check.tv_nsec);
		if (ns < RECHECK_NS)
			return;
	}
	last_check = now;
	struct stat st;
	int exists = stat(conf_path, &st) == 0;
	if (checked && exists == conf_exists && (!exists || same_file(&st, &conf_st)))
		return;
	checked = 1;
	conf_exists = exists;
	if (exists)
		conf_st = st;
	load_locked();
}

static void set_mode_state_locked(const char *s)
{
	if (strcmp(s, mode_state)) {
		snprintf(mode_state, sizeof mode_state, "%s", s);
		fprintf(stderr, "bdout: format %s\n", mode_state);
		write_status_locked();
	}
}

EXPORT bdout_drm_connector *drmModeGetConnector(int fd, uint32_t connector_id)
{
	pthread_once(&once, init);
	if (!real_get_connector) {
		errno = ENOSYS;
		return NULL;
	}
	bdout_drm_connector *c = real_get_connector(fd, connector_id);
	if (!c)
		return c;

	pthread_mutex_lock(&mu);
	refresh_locked();
	/* mode= says what PPApp was handed, not what shim.conf says now: a
	 * format only takes when PPApp asks again, and bdout reads this to tell
	 * whether the decoder needs a restart to match the kept format. */
	if (!conf.has_mode && c->connector_type == BDOUT_DRM_CONNECTOR_HDMIA)
		set_mode_state_locked("off: the screen's own modes");
	if (conf.has_mode && c->connector_type == BDOUT_DRM_CONNECTOR_HDMIA) {
		char s[160];
		if (c->connection != BDOUT_DRM_CONNECTED) {
			snprintf(s, sizeof s, "%s waiting: no screen connected", conf.mode.name);
			set_mode_state_locked(s);
		} else {
			/* libdrm frees the list with free(), so ours comes from malloc. */
			bdout_drm_mode *m = malloc(sizeof *m);
			if (m) {
				*m = conf.mode;
				free(c->modes);
				c->modes = m;
				c->count_modes = 1;
				snprintf(s, sizeof s, "forced %s at %u Hz (%u kHz), in place of the screen's own list",
					 conf.mode.name, conf.mode.vrefresh, conf.mode.clock);
				set_mode_state_locked(s);
			}
		}
	}
	pthread_mutex_unlock(&mu);
	return c;
}

EXPORT int drmModeAddFB2(int fd, uint32_t width, uint32_t height, uint32_t pixel_format,
			 const uint32_t bo_handles[4], const uint32_t pitches[4], const uint32_t offsets[4],
			 uint32_t *buf_id, uint32_t flags)
{
	pthread_once(&once, init);
	if (!real_add_fb2) {
		errno = ENOSYS;
		return -ENOSYS;
	}
	int rc = real_add_fb2(fd, width, height, pixel_format, bo_handles, pitches, offsets, buf_id, flags);
	if (rc == 0 && buf_id) {
		pthread_mutex_lock(&mu);
		fbs[fb_next].id = *buf_id;
		fbs[fb_next].fourcc = pixel_format;
		fb_next = (fb_next + 1) % FB_SLOTS;
		pthread_mutex_unlock(&mu);
	}
	return rc;
}

EXPORT int drmModeRmFB(int fd, uint32_t buffer_id)
{
	pthread_once(&once, init);
	if (!real_rm_fb) {
		errno = ENOSYS;
		return -ENOSYS;
	}
	pthread_mutex_lock(&mu);
	for (unsigned i = 0; i < FB_SLOTS; i++)
		if (fbs[i].id == buffer_id)
			fbs[i].id = 0;
	pthread_mutex_unlock(&mu);
	return real_rm_fb(fd, buffer_id);
}

static int fb_is_video_locked(uint32_t id)
{
	if (!id)
		return 0;
	for (unsigned i = 0; i < FB_SLOTS; i++)
		if (fbs[i].id == id)
			return bdout_fourcc_is_yuv(fbs[i].fourcc);
	return 0;
}

/* The size of what PPApp is showing, whether or not an AOI is on: the tab
 * measures a crop against it. Written out only when it changes. */
static void note_picture_locked(uint32_t pw, uint32_t ph, uint32_t sw, uint32_t sh)
{
	char p[64];
	snprintf(p, sizeof p, "%ux%u on %ux%u", pw, ph, sw, sh);
	if (strcmp(p, picture)) {
		snprintf(picture, sizeof picture, "%s", p);
		write_status_locked();
	}
}

EXPORT int drmModeSetPlane(int fd, uint32_t plane_id, uint32_t crtc_id, uint32_t fb_id, uint32_t flags,
			   int32_t crtc_x, int32_t crtc_y, uint32_t crtc_w, uint32_t crtc_h,
			   uint32_t src_x, uint32_t src_y, uint32_t src_w, uint32_t src_h)
{
	pthread_once(&once, init);
	if (!real_set_plane) {
		errno = ENOSYS;
		return -ENOSYS;
	}

	pthread_mutex_lock(&mu);
	refresh_locked();
	/* PPApp puts its video on the screen as a whole NV12 frame at the origin;
	 * its OSD is ARGB and placed where it belongs. Only the first is ours. */
	int video = crtc_x == 0 && crtc_y == 0 && fb_is_video_locked(fb_id);
	if (video)
		note_picture_locked(src_w >> 16, src_h >> 16, crtc_w, crtc_h);
	int ours = video && conf.has_aoi && !aoi_failed;
	bdout_conf c = conf;
	pthread_mutex_unlock(&mu);
	if (!ours)
		return real_set_plane(fd, plane_id, crtc_id, fb_id, flags, crtc_x, crtc_y, crtc_w, crtc_h,
				      src_x, src_y, src_w, src_h);

	bdout_plane in = {crtc_x, crtc_y, crtc_w, crtc_h, src_x, src_y, src_w, src_h}, out;
	char err[200];
	if (bdout_apply_aoi(&c, &in, &out, err, sizeof err)) {
		pthread_mutex_lock(&mu);
		aoi_failed = 1;
		snprintf(aoi_state, sizeof aoi_state, "not applied: %s", err);
		fprintf(stderr, "bdout: AOI %s\n", aoi_state);
		write_status_locked();
		pthread_mutex_unlock(&mu);
		return real_set_plane(fd, plane_id, crtc_id, fb_id, flags, crtc_x, crtc_y, crtc_w, crtc_h,
				      src_x, src_y, src_w, src_h);
	}

	int rc = real_set_plane(fd, plane_id, crtc_id, fb_id, flags, out.crtc_x, out.crtc_y, out.crtc_w,
				out.crtc_h, out.src_x, out.src_y, out.src_w, out.src_h);
	char geom[160];
	snprintf(geom, sizeof geom, "%ux%u at %u,%u of a %ux%u picture -> %ux%u at %d,%d of %ux%u",
		 out.src_w >> 16, out.src_h >> 16, out.src_x >> 16, out.src_y >> 16, src_w >> 16, src_h >> 16,
		 out.crtc_w, out.crtc_h, out.crtc_x, out.crtc_y, crtc_w, crtc_h);
	pthread_mutex_lock(&mu);
	if (rc) {
		/* The kernel said no. Show the frame as PPApp asked and stop trying
		 * until the settings change, rather than fail every frame. */
		aoi_failed = 1;
		snprintf(aoi_state, sizeof aoi_state, "refused by the display (error %d) for %s", rc, geom);
		fprintf(stderr, "bdout: AOI %s\n", aoi_state);
		write_status_locked();
	} else if (strcmp(geom, aoi_geom)) {
		snprintf(aoi_geom, sizeof aoi_geom, "%s", geom);
		snprintf(aoi_state, sizeof aoi_state, "on");
		write_status_locked();
	}
	pthread_mutex_unlock(&mu);
	if (rc)
		return real_set_plane(fd, plane_id, crtc_id, fb_id, flags, crtc_x, crtc_y, crtc_w, crtc_h,
				      src_x, src_y, src_w, src_h);
	return 0;
}
