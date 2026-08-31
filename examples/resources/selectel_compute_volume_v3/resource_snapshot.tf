# Create a network volume from a snapshot
resource "selectel_compute_volume_v3" "volume_1" {
  project_id  = selectel_vpc_project_v2.project_1.id
  region      = "ru-1"
  size        = 20
  snapshot_id = data.selectel_compute_snapshot_v3.snapshot_1.id
}
