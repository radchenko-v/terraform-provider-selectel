---
layout: "selectel"
page_title: "Selectel: selectel_compute_volume_v3"
sidebar_current: "docs-selectel-resource-compute-volume-v3"
description: |-
  Creates and manages a network volume in Selectel Cloud Servers using public OpenStack Block Storage API v3.
---

# selectel\_compute\_volume_v3

Creates and manages a network volume in Selectel Cloud Servers using public OpenStack Block Storage API v3. For more information about network volumes, see the [official Selectel documentation](https://docs.selectel.ru/cloud-servers/volumes/about-network-volumes/).

## Example Usage

### Create an empty volume

```hcl
resource "selectel_compute_volume_v3" "volume_1" {
  project_id = selectel_vpc_project_v2.project_1.id
  region     = "ru-1"
  size       = 10
}
```

### Create a Fast v2 volume with custom IOPS

```hcl
resource "selectel_compute_volume_v3" "volume_1" {
  project_id        = selectel_vpc_project_v2.project_1.id
  region            = "ru-6"
  size              = 10
  availability_zone = "ru-6a"
  volume_type       = "fast2.ru-6a"

  metadata = {
    total_iops_sec = "30000"
  }
}
```

### Create a volume from a snapshot

```hcl
resource "selectel_compute_volume_v3" "volume_1" {
  project_id  = selectel_vpc_project_v2.project_1.id
  region      = "ru-1"
  size        = 20
  snapshot_id = data.openstack_blockstorage_snapshot_v3.snapshot_1.id
}
```

## Argument Reference

* `project_id` - (Required) Unique identifier of the associated project. Changing this creates a new volume. Retrieved from the [selectel_vpc_project_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/vpc_project_v2) resource. Learn more about [Projects](https://docs.selectel.ru/access-control/projects/about-projects/).

* `region` - (Required) Pool where the volume is located, for example, `ru-1`. Changing this creates a new volume. Learn more about available pools in the [Availability matrix](https://docs.selectel.ru/infrastructure/availability-matrix/#network-volumes).

* `size` - (Required) Volume size in GB. Must be at least `1`. When `snapshot_id`, `source_vol_id`, `image_id`, or `backup_id` is set, the size must be equal to or greater than the source size. The maximum size depends on the volume type, pool segment, and whether the volume is used as a boot or additional volume. Learn more about maximum sizes in the [Network volume limits](https://docs.selectel.ru/cloud-servers/volumes/about-network-volumes/#network-volume-limits). You can only increase the size. To increase the size of an attached volume, set `enable_online_resize` to `true`. Learn more about [increasing a network volume](https://docs.selectel.ru/cloud-servers/volumes/edit-volume/#enlarge-volume).

* `enable_online_resize` - (Optional) Enables increasing the size of an attached volume. Boolean flag, the default value is `false`. If set to `false`, detach the volume before increasing its size. Learn more about [increasing a network volume](https://docs.selectel.ru/cloud-servers/volumes/edit-volume/#enlarge-volume).

* `name` - (Optional) Volume name.

* `description` - (Optional) Volume description.

* `availability_zone` - (Optional) Pool segment where the volume is located, for example, `ru-1a`. Changing this creates a new volume. If omitted, the Block Storage API selects a pool segment. Learn more about available pool segments in the [Availability matrix](https://docs.selectel.ru/infrastructure/availability-matrix/#network-volumes).

* `volume_type` - (Optional) Volume type, for example, `fast.ru-1a`. Changing this creates a new volume. Available type prefixes are `basic`, `basicssd`, `universal`, `universal2`, `fast`, and `fast2`. The zonal type format is `<volume_type>.<pool_segment>`. We recommend specifying the zonal type name that matches `availability_zone`. If the API replaces a regional suffix with the volume availability zone, the provider treats the returned name as the same type. If omitted, the Block Storage API selects the default type. Learn more about available types in the [Network volume types list](https://docs.selectel.ru/cloud-servers/volumes/about-network-volumes/#network-volume-types-list).

* `metadata` - (Optional) Key-value pairs associated with the volume. Set `total_iops_sec` to configure total read and write IOPS for a Universal v2 or Fast v2 volume. Available values are from `2000` to `16000` for Universal v2 and from `25000` to `75000` for Fast v2. The default values are `2000` and `25000`, respectively. The key `selectel_tf_create_token` is reserved by the provider and cannot be configured. The API can add other service metadata, which the provider publishes in the state. The key `total_bytes_sec` is managed by the API. After the API returns this key, the provider preserves its value and ignores attempts to change or remove it from the configuration. Learn more about available values in the [Network volume limits](https://docs.selectel.ru/cloud-servers/volumes/about-network-volumes/#network-volume-limits).

* `snapshot_id` - (Optional) Unique identifier of a snapshot to use as the volume source. Changing this creates a new volume. Conflicts with `source_vol_id`, `image_id`, and `backup_id`. You can retrieve the ID with the [openstack_blockstorage_snapshot_v3](https://registry.terraform.io/providers/terraform-provider-openstack/openstack/latest/docs/data-sources/blockstorage_snapshot_v3) data source.

* `source_vol_id` - (Optional) Unique identifier of another volume to copy. Changing this creates a new volume. Conflicts with `snapshot_id`, `image_id`, and `backup_id`. You can retrieve the ID from the [selectel_compute_volume_v3 resource](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/compute_volume_v3) or [selectel_compute_volume_v3 data source](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/compute_volume_v3).

* `image_id` - (Optional) Unique identifier of an image to use as the volume source. Changing this creates a new volume. Conflicts with `snapshot_id`, `source_vol_id`, and `backup_id`. You can retrieve the ID with the [openstack_images_image_v2](https://registry.terraform.io/providers/terraform-provider-openstack/openstack/latest/docs/data-sources/images_image_v2) data source.

* `backup_id` - (Optional) Unique identifier of a backup to restore. Changing this creates a new volume. Conflicts with `snapshot_id`, `source_vol_id`, and `image_id`. You can retrieve the ID from the `backup_id` attribute of the [selectel_cloudbackup_checkpoint_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/cloudbackup_checkpoint_v2) data source.

## Attributes Reference

* `attachment` - Volume attachments observed through the Block Storage API. This attribute is read-only; attach and detach the volume through the Cloud Servers API or other supported tools. Detach the volume before destroying it. If the volume is attached, Terraform returns an error and keeps it in the state.

  * `id` - Unique identifier of the volume. The Block Storage API repeats the volume ID in each attachment record; this is not an attachment ID.

  * `instance_id` - Unique identifier of the cloud server.

  * `device` - Device name on the cloud server.

## Import

You can import a volume by its unique identifier. Set the project and pool explicitly because they cannot be derived from the volume ID:

```shell
export OS_AUTH_URL=<keystone_url>
export OS_REGION_NAME=<authentication_pool>
export OS_DOMAIN_NAME=<account_id>
export OS_USERNAME=<username>
export OS_PASSWORD=<password>
export INFRA_PROJECT_ID=<project_id>
export INFRA_REGION=<volume_pool>

terraform import selectel_compute_volume_v3.volume_1 <volume_id>
```

where:

* `<keystone_url>` — Keystone Identity authentication URL, for example, `https://cloud.api.selcloud.ru/identity/v3/`.

* `<authentication_pool>` — Pool where the Keystone API and Resell API endpoints are located, for example, `ru-1`. This value does not determine the volume pool. Learn more about available pools in the [Availability matrix](https://docs.selectel.ru/infrastructure/availability-matrix/).

* `<account_id>` — Selectel account ID. The account ID is in the top right corner of the [Control panel](https://my.selectel.ru/). Learn more about [Registration](https://docs.selectel.ru/account/registration/).

* `<username>` — Name of the service user. To get the name, in the [Control panel](https://my.selectel.ru/iam/service-users), go to **IAM** ⟶ **Service users** ⟶ copy the name of the required user. Learn more about [Service users](https://docs.selectel.ru/access-control/user-types/).

* `<password>` — Password of the service user.

* `<project_id>` — Unique identifier of the associated project. To get the project ID, in the [Control panel](https://my.selectel.ru/vpc/), go to **Products** ⟶ **Cloud Servers** ⟶ open the projects menu ⟶ copy the ID in the row of the required project. Learn more about [Projects](https://docs.selectel.ru/access-control/projects/about-projects/).

* `<volume_pool>` — Pool where the volume is located, for example, `ru-1`. Learn more about available pools in the [Availability matrix](https://docs.selectel.ru/infrastructure/availability-matrix/#network-volumes).

* `<volume_id>` — Unique identifier of the volume. To get the volume ID, use the `openstack --os-region-name <volume_pool> volume list` command. Learn more about [OpenStack CLI](https://docs.selectel.ru/cloud-servers/tools/openstack-cli/).
