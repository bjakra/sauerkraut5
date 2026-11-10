// SPDX-License-Identifier: MIT
//
// The part of the Mali kbase kernel interface (Midgard r26p0, UK 11.11, the ioctl form that replaced
// the older "UK" calls) that TECHO5's tools use on the 1st gen Show 5. Written from the interface as
// the GPLv2 driver in Amazon's kernel tree defines it
// (drivers/misc/mediatek/gpu/gpu_mali/mali_midgard/midgard-r26p0/gpu/arm/midgard: mali_kbase_ioctl.h
// and mali_base_kernel.h); only numbers and layouts, which is what a caller has to match.

#ifndef TECHO5_KBASE_UAPI_H
#define TECHO5_KBASE_UAPI_H

#include <stdint.h>
#include <sys/ioctl.h>

#define KBASE_IOCTL_TYPE 0x80

struct kbase_ioctl_version_check {
	uint16_t major, minor;
};
#define KBASE_IOCTL_VERSION_CHECK _IOWR(KBASE_IOCTL_TYPE, 0, struct kbase_ioctl_version_check)

struct kbase_ioctl_set_flags {
	uint32_t create_flags;
};
#define KBASE_IOCTL_SET_FLAGS _IOW(KBASE_IOCTL_TYPE, 1, struct kbase_ioctl_set_flags)

struct kbase_ioctl_job_submit {
	uint64_t addr; // of nr_atoms struct kbase_atom, stride bytes apart
	uint32_t nr_atoms;
	uint32_t stride;
};
#define KBASE_IOCTL_JOB_SUBMIT _IOW(KBASE_IOCTL_TYPE, 2, struct kbase_ioctl_job_submit)

struct kbase_ioctl_get_gpuprops {
	uint64_t buffer;
	uint32_t size; // 0: the call returns the size needed
	uint32_t flags;
};
#define KBASE_IOCTL_GET_GPUPROPS _IOW(KBASE_IOCTL_TYPE, 3, struct kbase_ioctl_get_gpuprops)

union kbase_ioctl_mem_alloc {
	struct {
		uint64_t va_pages, commit_pages, extent, flags;
	} in;
	struct {
		uint64_t flags, gpu_va;
	} out;
};
#define KBASE_IOCTL_MEM_ALLOC _IOWR(KBASE_IOCTL_TYPE, 5, union kbase_ioctl_mem_alloc)

struct kbase_ioctl_mem_free {
	uint64_t gpu_addr;
};
#define KBASE_IOCTL_MEM_FREE _IOW(KBASE_IOCTL_TYPE, 7, struct kbase_ioctl_mem_free)

struct kbase_ioctl_get_ddk_version {
	uint64_t version_buffer;
	uint32_t size;
	uint32_t padding;
};
#define KBASE_IOCTL_GET_DDK_VERSION _IOW(KBASE_IOCTL_TYPE, 13, struct kbase_ioctl_get_ddk_version)

struct kbase_ioctl_mem_sync {
	uint64_t handle;    // the region's GPU address
	uint64_t user_addr; // CPU address of the part to sync
	uint64_t size;
	uint8_t type; // KBASE_SYNC_TO_*
	uint8_t padding[7];
};
#define KBASE_IOCTL_MEM_SYNC _IOW(KBASE_IOCTL_TYPE, 15, struct kbase_ioctl_mem_sync)
#define KBASE_SYNC_TO_DEVICE 1 // the CPU wrote, the GPU will read
#define KBASE_SYNC_TO_CPU 2    // the GPU wrote, the CPU will read

struct kbase_ioctl_mem_commit {
	uint64_t gpu_addr;
	uint64_t pages;
};
#define KBASE_IOCTL_MEM_COMMIT _IOW(KBASE_IOCTL_TYPE, 20, struct kbase_ioctl_mem_commit)

union kbase_ioctl_mem_import {
	struct {
		uint64_t flags;
		uint64_t phandle; // pointer to an int (a dma-buf fd) or to struct kbase_mem_import_user_buffer
		uint32_t type;
		uint32_t padding;
	} in;
	struct {
		uint64_t flags, gpu_va, va_pages;
	} out;
};
#define KBASE_IOCTL_MEM_IMPORT _IOWR(KBASE_IOCTL_TYPE, 22, union kbase_ioctl_mem_import)
#define KBASE_MEM_IMPORT_TYPE_UMM 2 // a dma-buf
#define KBASE_MEM_IMPORT_TYPE_USER_BUFFER 3

struct kbase_mem_import_user_buffer {
	uint64_t ptr;
	uint64_t length;
};

// Memory flags (base_mem_alloc_flags).
#define KBASE_MEM_PROT_CPU_RD (1u << 0)
#define KBASE_MEM_PROT_CPU_WR (1u << 1)
#define KBASE_MEM_PROT_GPU_RD (1u << 2)
#define KBASE_MEM_PROT_GPU_WR (1u << 3)
#define KBASE_MEM_PROT_GPU_EX (1u << 4)
#define KBASE_MEM_GROW_ON_GPF (1u << 9)
#define KBASE_MEM_COHERENT_SYSTEM (1u << 10)
#define KBASE_MEM_COHERENT_LOCAL (1u << 11)
#define KBASE_MEM_CACHED_CPU (1u << 12)
#define KBASE_MEM_SAME_VA (1u << 13) // GPU address = the address mmap gives
#define KBASE_MEM_NEED_MMAP (1u << 14)
#define KBASE_MEM_IMPORT_SHARED (1u << 18)

// mmap offsets with a meaning of their own.
#define KBASE_MEM_MAP_TRACKING_HANDLE (3ull << 12)

// One job chain handed to the kernel (base_jd_atom_v2).
struct kbase_atom {
	uint64_t jc; // GPU address of the first job header
	uint64_t udata[2];
	uint64_t extres_list;
	uint16_t nr_extres;
	uint16_t compat_core_req;
	struct {
		uint8_t atom_id;
		uint8_t type; // KBASE_DEP_*
	} pre_dep[2];
	uint8_t atom_number;
	uint8_t prio;
	uint8_t device_nr;
	uint8_t padding[1];
	uint32_t core_req; // KBASE_REQ_*
};

#define KBASE_DEP_DATA 1
#define KBASE_DEP_ORDER 2

#define KBASE_REQ_FS (1u << 0)
#define KBASE_REQ_CS (1u << 1)
#define KBASE_REQ_T (1u << 2)
#define KBASE_REQ_CF (1u << 3)
#define KBASE_REQ_V (1u << 4)
#define KBASE_REQ_COHERENT_GROUP (1u << 6)
#define KBASE_REQ_PERMON (1u << 7) // keep the cycle counter running
#define KBASE_REQ_EXTERNAL_RESOURCES (1u << 8)
#define KBASE_REQ_ONLY_COMPUTE (1u << 10)
#define KBASE_REQ_FS_AFBC (1u << 13)

// What read() on the device returns for each atom that finished (base_jd_event_v2).
struct kbase_event {
	uint32_t code;
	uint8_t atom_number;
	uint8_t padding[3];
	uint64_t udata[2];
};

#define KBASE_EVENT_DONE 0x01
#define KBASE_EVENT_TERMINATED 0x04
#define KBASE_EVENT_JOB_CANCELLED 0x4002
#define KBASE_EVENT_DRV_TERMINATED 0x7000

#endif
