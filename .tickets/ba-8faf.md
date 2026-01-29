---
id: ba-8faf
status: open
deps: [ba-dca5]
links: []
created: 2026-01-29T18:11:01Z
type: task
priority: 1
assignee: vot3k
parent: ba-3d4c
---
# Implement image and kernel management handlers

## Objective
Implement HTTP handlers for image and kernel management endpoints.

## Location
- Add to internal/api/handlers.go (or separate internal/api/handlers_images.go if handlers.go grows large)

## Implementation Details

### Image Endpoints

**GET /v1/images — List Images**
1. Call imgMgr.ListRootfs() for rootfs images
2. For each image, include name and file size
3. Return 200 with array

**POST /v1/images/pull — Pull Default Images**
1. Call imgMgr.EnsureDefaultImages()
2. Return 200 on success, 500 on download failure
3. This is a long-running operation — consider setting a longer write timeout

**POST /v1/images/import — Import Docker Image**
Request body:
```json
{
  "docker_image": "ubuntu:22.04",
  "name": "ubuntu-base",
  "size_mb": 2048
}
```
1. Validate docker_image and name fields are present
2. Check imgMgr.ImageExists(name) — return 409 if exists
3. Call imgMgr.ImportDockerImage(dockerImage, name, sizeMB)
4. Return 201 on success
5. Note: This can take 30-120 seconds (Docker pull + extract + install packages)
   Set appropriate write timeout or consider async with status polling

**DELETE /v1/images/{name} — Delete Image**
1. Check imgMgr.ImageExists(name) — return 404 if not found
2. Check no VMs reference this image — return 409 if in use
3. Call imgMgr.DeleteImage(name)
4. Return 200

### Kernel Endpoints

**GET /v1/kernels — List Kernels**
1. Call imgMgr.ListKernelsWithInfo()
2. Return 200 with array of KernelInfo structs

**POST /v1/kernels/import — Import Kernel**
Request: multipart/form-data with kernel binary file and name field
OR request body:
```json
{
  "source_path": "/path/to/vmlinux",
  "name": "kernel-6.1",
  "force": false
}
```
1. Validate name and source path
2. Call imgMgr.ImportKernel(srcPath, name, force)
3. Return 201 on success

**DELETE /v1/kernels/{name} — Delete Kernel**
1. Check imgMgr.KernelExists(name) — return 404 if not found
2. Check no VMs reference this kernel — return 409 if in use
3. Call imgMgr.DeleteKernel(name)
4. Return 200

## Implementation Notes
- Image import is the only potentially long-running endpoint
- For v1, synchronous with extended timeout is acceptable (2-5 minute WriteTimeout for import)
- Kernel import requires source_path to exist on the host filesystem
- Always check for in-use images/kernels before deletion (scan all VMs)

## Acceptance Criteria
- All 7 image/kernel endpoints respond correctly
- Image import with Docker creates a usable rootfs image
- Deleting an image/kernel in use by a VM returns 409 Conflict
- List endpoints return empty arrays (not null) when no items exist

## Acceptance Criteria

All 7 image/kernel endpoints work; in-use deletion returns 409; lists return [] not null

