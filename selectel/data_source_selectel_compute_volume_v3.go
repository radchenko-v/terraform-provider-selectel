package selectel

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	blockstorage "github.com/selectel/blockstorage-go/pkg/v1"
	"github.com/selectel/blockstorage-go/pkg/v1/volume"
)

func dataSourceComputeVolumeV3() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceComputeVolumeV3Read,
		Description: "Provides a network volume in Cloud Servers. " +
			"If `volume_id` is omitted, the data source searches the network volumes in the project and pool by the specified criteria and requires exactly one match. " +
			"With no criteria, the project and pool must contain exactly one network volume. " +
			"For more information about network volumes, see the " +
			"[official Selectel documentation](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/).",
		Schema: map[string]*schema.Schema{
			"id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Unique identifier of the found network volume.",
			},
			"project_id": computeV3ProjectIDDataSourceSchema(),
			"region":     computeV3RegionDataSourceSchema("network volume"),
			"volume_id": {
				Type:          schema.TypeString,
				Optional:      true,
				ConflictsWith: []string{"name", "status", "metadata"},
				ValidateFunc:  validation.StringIsNotWhiteSpace,
				Description: "Unique identifier of the network volume. " +
					"If set, the data source reads the network volume directly. " +
					"Conflicts with `name`, `status`, and `metadata`. " +
					"You can retrieve the ID from the [selectel_compute_volume_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/compute_volume_v3) resource.",
			},
			"name": {
				// The selected volume's name is also available as a search criterion.
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringIsNotWhiteSpace,
				Description: "Exact name of the network volume to search for. " +
					"If omitted, contains the name of the found network volume. " +
					"Conflicts with `volume_id`.",
			},
			"status": {
				// The selected volume's status is also available as a search criterion.
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.StringIsNotWhiteSpace,
				Description: "Exact status of the network volume to search for, for example, `available`. " +
					"If omitted, contains the status of the found network volume. " +
					"Conflicts with `volume_id`.",
			},
			"metadata": {
				// The selected volume's metadata is also available as a search criterion.
				Type:             schema.TypeMap,
				Optional:         true,
				Computed:         true,
				Elem:             &schema.Schema{Type: schema.TypeString},
				ValidateDiagFunc: validateComputeVolumeV3Metadata,
				Description: "Key-value pairs that the network volume metadata must contain. " +
					"Other metadata keys do not prevent a match. " +
					"The `selectel_tf_create_token` key is reserved by the provider and cannot be used as a search criterion. " +
					"After the search, contains the metadata of the found network volume returned by the Block Storage API, except the `selectel_tf_create_token` key. " +
					"Conflicts with `volume_id`.",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Network volume description.",
			},
			"size": {
				Type:        schema.TypeInt,
				Computed:    true,
				Description: "Network volume size in GB.",
			},
			"availability_zone": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Pool segment where the network volume is located.",
			},
			"volume_type": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Network volume type.",
			},
			"bootable": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "Whether the network volume is bootable. " +
					"The Block Storage API returns the value as a string, `true` or `false`.",
			},
			"snapshot_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Unique identifier of the source snapshot if the network volume was created from a snapshot.",
			},
			"source_vol_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Unique identifier of the source network volume if the network volume was copied from another network volume.",
			},
			"attachment": {
				Type:     schema.TypeSet,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"instance_id": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"device": {
							Type:     schema.TypeString,
							Computed: true,
						},
					},
				},
				Description: "Network volume attachments. " + computeVolumeV3AttachmentDescription,
			},
		},
	}
}

func dataSourceComputeVolumeV3Read(
	ctx context.Context,
	d *schema.ResourceData,
	meta any,
) diag.Diagnostics {
	client, diagnostics := getBlockStorageClient(d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	return readComputeVolumeLookupV3(ctx, d, client)
}

func readComputeVolumeLookupV3(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
) diag.Diagnostics {
	var selected *volume.View

	if volumeID, ok := d.GetOk("volume_id"); ok {
		var err error

		selected, _, err = volume.Get(ctx, client, volumeID.(string))
		if err != nil {
			if blockstorage.IsKind(err, blockstorage.KindNotFound) {
				return diag.Errorf(
					"Block Storage volume %q does not exist or is not accessible to the current role: %v",
					volumeID,
					err,
				)
			}

			return blockStorageOperationDiagnostics("read the volume by ID", err)
		}
	} else {
		volumes, err := volume.ListDetail(ctx, client, volume.ListOpts{})
		if err != nil {
			return blockStorageOperationDiagnostics("read the complete volume list", err)
		}

		matches := make([]*volume.View, 0, 1)
		for i := range volumes {
			if matchesComputeVolumeLookupV3(d, &volumes[i]) {
				matches = append(matches, &volumes[i])
			}
		}

		switch len(matches) {
		case 0:
			return diag.Errorf("no Block Storage volumes matched the configured search criteria; " +
				"check project_id, region, access permissions, and any name, status, or metadata filters. " +
				"To look up a known volume_id, remove the conflicting filters")
		case 1:
			selected = matches[0]
		default:
			return diag.Errorf(
				"found %d Block Storage volumes matching the configured search criteria; "+
					"use volume_id for an unambiguous lookup",
				len(matches),
			)
		}
	}

	if err := setComputeVolumeLookupV3(d, selected); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

func matchesComputeVolumeLookupV3(d *schema.ResourceData, candidate *volume.View) bool {
	if name, ok := d.GetOk("name"); ok && candidate.Name != name.(string) {
		return false
	}
	if status, ok := d.GetOk("status"); ok && candidate.Status != status.(string) {
		return false
	}
	if metadata, ok := d.GetOk("metadata"); ok {
		for key, value := range expandComputeVolumeV3StringMap(metadata) {
			candidateValue, exists := candidate.Metadata[key]
			if !exists || candidateValue != value {
				return false
			}
		}
	}

	return true
}

func setComputeVolumeLookupV3(d *schema.ResourceData, selected *volume.View) error {
	if err := setComputeVolumeV3(d, selected); err != nil {
		return err
	}
	if err := d.Set("snapshot_id", selected.SnapshotID); err != nil {
		return fmt.Errorf("failed to set Block Storage volume snapshot_id: %w", err)
	}
	if err := d.Set("source_vol_id", selected.SourceVolID); err != nil {
		return fmt.Errorf("failed to set Block Storage volume source_vol_id: %w", err)
	}
	if err := d.Set("status", selected.Status); err != nil {
		return fmt.Errorf("failed to set Block Storage volume status: %w", err)
	}
	if err := d.Set("bootable", selected.Bootable); err != nil {
		return fmt.Errorf("failed to set Block Storage volume bootable: %w", err)
	}

	d.SetId(selected.ID)

	return nil
}
