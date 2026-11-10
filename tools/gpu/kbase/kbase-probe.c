// SPDX-License-Identifier: MIT
//
// kbase-probe: talks to the Mali kbase driver (/dev/mali0) the way a GPU driver in user space has to,
// with no library of Arm's or Amazon's: the version handshake, the GPU's properties, memory in each of
// the address zones, and one job chain run on the GPU (a "write value" job, which needs no shader),
// with its completion event read back. It changes nothing on the device; everything it allocates goes
// when it exits.
//
//   kbase-probe [-n runs]

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

#include "kbase_uapi.h"

static double now_ms(void)
{
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e3 + t.tv_nsec / 1e6;
}

#define die(...) do { fprintf(stderr, "kbase-probe: " __VA_ARGS__); fprintf(stderr, ": %s\n", strerror(errno)); exit(1); } while (0)

static const char *prop_name(unsigned id)
{
	switch (id) {
	case 1: return "product_id";
	case 2: return "version_status";
	case 3: return "minor_revision";
	case 4: return "major_revision";
	case 6: return "gpu_freq_khz_max";
	case 8: return "log2_program_counter_size";
	case 9: return "texture_features_0";
	case 10: return "texture_features_1";
	case 11: return "texture_features_2";
	case 12: return "gpu_available_memory_size";
	case 16: return "tiler_bin_size_bytes";
	case 17: return "tiler_max_active_levels";
	case 18: return "max_threads";
	case 25: return "raw_shader_present";
	case 26: return "raw_tiler_present";
	case 27: return "raw_l2_present";
	case 29: return "raw_l2_features";
	case 30: return "raw_core_features";
	case 31: return "raw_mem_features";
	case 32: return "raw_mmu_features";
	case 33: return "raw_as_present";
	case 34: return "raw_js_present";
	case 35: return "raw_js_features_0";
	case 36: return "raw_js_features_1";
	case 37: return "raw_js_features_2";
	case 51: return "raw_tiler_features";
	case 52: return "raw_texture_features_0";
	case 53: return "raw_texture_features_1";
	case 54: return "raw_texture_features_2";
	case 55: return "raw_gpu_id";
	case 56: return "raw_thread_max_threads";
	case 59: return "raw_thread_features";
	case 60: return "raw_coherency_mode";
	case 61: return "coherency_num_groups";
	case 62: return "coherency_num_core_groups";
	case 63: return "coherency_coherency";
	case 64: return "coherency_group_0";
	default: return NULL;
	}
}

static void props(int fd)
{
	struct kbase_ioctl_get_gpuprops g = {0};
	int size = ioctl(fd, KBASE_IOCTL_GET_GPUPROPS, &g);
	if (size <= 0)
		die("GET_GPUPROPS size");
	uint8_t *buf = malloc(size);
	g.buffer = (uintptr_t)buf;
	g.size = size;
	if (ioctl(fd, KBASE_IOCTL_GET_GPUPROPS, &g) < 0)
		die("GET_GPUPROPS");
	printf("gpuprops: %d bytes\n", size);
	for (int i = 0; i + 4 <= size;) {
		uint32_t key;
		memcpy(&key, buf + i, 4);
		i += 4;
		unsigned id = key >> 2, w = 1u << (key & 3);
		uint64_t v = 0;
		memcpy(&v, buf + i, w);
		i += w;
		const char *n = prop_name(id);
		if (n)
			printf("  %-28s 0x%" PRIx64 "\n", n, v);
	}
	free(buf);
}

struct mem {
	uint64_t gpu;
	void *cpu;
	size_t size;
};

// alloc is one region: flags as given; with KBASE_MEM_SAME_VA the kernel hands out a cookie and the
// address mmap picks becomes the GPU's too, without it the kernel picks the GPU address itself.
static struct mem alloc(int fd, unsigned pages, uint32_t flags, const char *what)
{
	union kbase_ioctl_mem_alloc a = {.in = {.va_pages = pages, .commit_pages = pages, .flags = flags}};
	struct mem m = {0};
	if (ioctl(fd, KBASE_IOCTL_MEM_ALLOC, &a) < 0)
		die("MEM_ALLOC %s", what);
	m.size = (size_t)pages * 4096;
	int prot = (flags & KBASE_MEM_PROT_CPU_RD ? PROT_READ : 0) | (flags & KBASE_MEM_PROT_CPU_WR ? PROT_WRITE : 0);
	m.cpu = mmap(NULL, m.size, prot, MAP_SHARED, fd, (off_t)a.out.gpu_va);
	if (m.cpu == MAP_FAILED)
		die("mmap %s (handle 0x%" PRIx64 ")", what, (uint64_t)a.out.gpu_va);
	m.gpu = flags & KBASE_MEM_SAME_VA ? (uintptr_t)m.cpu : a.out.gpu_va;
	printf("alloc %-22s flags in 0x%05x out 0x%05" PRIx64 "  gpu 0x%010" PRIx64 "  cpu %p\n", what, flags,
	       (uint64_t)a.out.flags, m.gpu, m.cpu);
	return m;
}

int main(int argc, char **argv)
{
	int runs = 3;
	if (argc == 3 && !strcmp(argv[1], "-n"))
		runs = atoi(argv[2]);

	int fd = open("/dev/mali0", O_RDWR | O_CLOEXEC);
	if (fd < 0)
		die("open /dev/mali0");

	struct kbase_ioctl_version_check v = {11, 11};
	if (ioctl(fd, KBASE_IOCTL_VERSION_CHECK, &v) < 0)
		die("VERSION_CHECK");
	printf("kbase UK version %u.%u\n", v.major, v.minor);

	struct kbase_ioctl_set_flags f = {0};
	if (ioctl(fd, KBASE_IOCTL_SET_FLAGS, &f) < 0)
		die("SET_FLAGS");

	char ddk[64] = "";
	struct kbase_ioctl_get_ddk_version d = {.version_buffer = (uintptr_t)ddk, .size = sizeof ddk};
	if (ioctl(fd, KBASE_IOCTL_GET_DDK_VERSION, &d) >= 0)
		printf("ddk %s\n", ddk);

	// The tracking page: the driver accounts the context's memory to the process that maps it, and
	// refuses to allocate before it is mapped.
	void *track = mmap(NULL, 4096, PROT_NONE, MAP_SHARED, fd, KBASE_MEM_MAP_TRACKING_HANDLE);
	if (track == MAP_FAILED)
		die("mmap tracking page");

	props(fd);

	uint32_t rw = KBASE_MEM_PROT_CPU_RD | KBASE_MEM_PROT_CPU_WR | KBASE_MEM_PROT_GPU_RD | KBASE_MEM_PROT_GPU_WR;
	struct mem same = alloc(fd, 4, rw | KBASE_MEM_SAME_VA, "same-va");
	struct mem custom = alloc(fd, 4, rw, "custom-va");
	struct mem cached = alloc(fd, 4, rw | KBASE_MEM_SAME_VA | KBASE_MEM_CACHED_CPU, "same-va cpu-cached");
	struct mem exec = alloc(fd, 4, KBASE_MEM_PROT_CPU_RD | KBASE_MEM_PROT_CPU_WR | KBASE_MEM_PROT_GPU_RD | KBASE_MEM_PROT_GPU_EX,
				"custom-va executable");
	(void)exec;

	// A chain of two "write value" jobs in each kind of region: the first zeroes a word, the second
	// stores the GPU's cycle counter in another (the counter only runs for an atom that asks for
	// KBASE_REQ_PERMON). The GPU writes each job's status into its header: 1 is done.
	struct mem *regions[] = {&same, &custom, &cached};
	const char *names[] = {"same-va", "custom-va", "cpu-cached"};
	uint8_t atom_nr = 1;
	int ok = 1;
	for (int run = 0; run < runs; run++) {
		for (int r = 0; r < 3; r++) {
			struct mem *m = regions[r];
			uint32_t *w = m->cpu;
			memset(w, 0, 512);
			uint32_t *zeroed = w + 96, *cycles = w + 98;
			zeroed[0] = 0xdeadbeef;
			cycles[0] = cycles[1] = 0;
			for (int j = 0; j < 2; j++) {
				uint32_t *job = w + j * 16; // 64 bytes each: header (32) then payload
				uint64_t next = j == 0 ? m->gpu + 64 : 0;
				uint64_t target = m->gpu + (j == 0 ? 96 : 98) * 4;
				job[4] = 1 /* 64-bit descriptor */ | 2 << 1 /* write value */ | (uint32_t)(j + 1) << 16;
				job[6] = (uint32_t)next;
				job[7] = (uint32_t)(next >> 32);
				job[8] = (uint32_t)target;
				job[9] = (uint32_t)(target >> 32);
				job[10] = j == 0 ? 3 /* zero */ : 1 /* cycle counter */;
			}
			if (m == &cached) {
				struct kbase_ioctl_mem_sync s = {.handle = m->gpu, .user_addr = (uintptr_t)m->cpu, .size = 512, .type = KBASE_SYNC_TO_DEVICE};
				if (ioctl(fd, KBASE_IOCTL_MEM_SYNC, &s) < 0)
					die("MEM_SYNC to device");
			}
			struct kbase_atom atom = {.jc = m->gpu, .udata = {0x1234, run}, .atom_number = atom_nr, .core_req = KBASE_REQ_CS | KBASE_REQ_T | KBASE_REQ_PERMON};
			struct kbase_ioctl_job_submit sub = {.addr = (uintptr_t)&atom, .nr_atoms = 1, .stride = sizeof atom};
			double t0 = now_ms();
			if (ioctl(fd, KBASE_IOCTL_JOB_SUBMIT, &sub) < 0)
				die("JOB_SUBMIT");
			struct pollfd p = {.fd = fd, .events = POLLIN};
			int n = poll(&p, 1, 3000);
			if (n <= 0) {
				printf("%-10s run %d: no event within 3 s\n", names[r], run);
				return 2;
			}
			struct kbase_event ev;
			if (read(fd, &ev, sizeof ev) != sizeof ev)
				die("read event");
			double t1 = now_ms();
			if (m == &cached) {
				struct kbase_ioctl_mem_sync s = {.handle = m->gpu, .user_addr = (uintptr_t)m->cpu, .size = 512, .type = KBASE_SYNC_TO_CPU};
				if (ioctl(fd, KBASE_IOCTL_MEM_SYNC, &s) < 0)
					die("MEM_SYNC to cpu");
			}
			uint64_t cyc = cycles[0] | (uint64_t)cycles[1] << 32;
			int good = ev.code == KBASE_EVENT_DONE && ev.atom_number == atom_nr && w[0] == 1 && w[16] == 1 && zeroed[0] == 0 && cyc != 0;
			printf("%-10s run %d: event 0x%x atom %u udata 0x%" PRIx64 "  status 0x%x 0x%x  zeroed 0x%x  cycles %" PRIu64 "  %.2f ms  %s\n",
			       names[r], run, ev.code, ev.atom_number, (uint64_t)ev.udata[0], w[0], w[16], zeroed[0], cyc, t1 - t0, good ? "ok" : "FAILED");
			ok &= good;
			if (++atom_nr == 0)
				atom_nr = 1;
		}
	}

	for (int r = 0; r < 3; r++)
		munmap(regions[r]->cpu, regions[r]->size);
	struct kbase_ioctl_mem_free fr = {.gpu_addr = custom.gpu};
	if (ioctl(fd, KBASE_IOCTL_MEM_FREE, &fr) < 0)
		die("MEM_FREE custom-va");
	close(fd);
	printf("%s\n", ok ? "PASS" : "FAIL");
	return ok ? 0 : 1;
}
