package selectel

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	blockstorage "github.com/selectel/blockstorage-go/pkg/v1"
	"github.com/selectel/blockstorage-go/pkg/v1/volumetype"
)

func dataSourceComputeVolumeTypeV3() *schema.Resource {
	const (
		volumeTypeIDAttribute = "volume_type_id"
		nameAttribute         = "name"
	)

	return &schema.Resource{
		ReadContext: dataSourceComputeVolumeTypeV3Read,
		Description: "Provides a network volume type in Cloud Servers. " +
			"For more information about network volume types, see the " +
			"[official Selectel documentation](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-types-list).",
		Schema: map[string]*schema.Schema{
			"id": {
				Type:     schema.TypeString,
				Computed: true,
				Description: "Unique identifier of the found volume type. " +
					"If `volume_type_id` is `default`, contains the UUID resolved by the API rather than `default`.",
			},
			"project_id": computeV3ProjectIDDataSourceSchema(),
			"region":     computeV3RegionDataSourceSchema("volume type"),
			volumeTypeIDAttribute: {
				Type:         schema.TypeString,
				Optional:     true,
				ExactlyOneOf: []string{volumeTypeIDAttribute, nameAttribute},
				ValidateFunc: validation.StringIsNotWhiteSpace,
				Description: "Unique identifier of the volume type or the `default` value. " +
					"If `default`, the data source finds the default volume type: the project default when configured, otherwise the platform default. " +
					"To get the volume type ID, use the `openstack --os-region-name <region> volume type list` command. " +
					"Conflicts with `name`. Exactly one of `volume_type_id` or `name` is required. " +
					"Learn more about available types in the [Network volume types list](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-types-list).",
			},
			nameAttribute: {
				// The API returns the name when the volume type is selected by ID.
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				ExactlyOneOf: []string{volumeTypeIDAttribute, nameAttribute},
				ValidateFunc: validation.StringIsNotWhiteSpace,
				Description: "Exact name of the volume type. " +
					"Available type prefixes are `basic`, `basicssd`, `universal`, `universal2`, `fast`, and `fast2`. " +
					"The format is `<volume_type>.<pool_segment>`. " +
					"The data source reads the complete public type list and requires exactly one match. " +
					"If `volume_type_id` is set, contains the name of the found volume type. " +
					"Conflicts with `volume_type_id`. Exactly one of `volume_type_id` or `name` is required. " +
					"Learn more about available types in the [Network volume types list](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-types-list).",
			},
			"description": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Volume type description.",
			},
			"is_public": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "Whether the volume type is public.",
			},
			"extra_specs": {
				Type:     schema.TypeMap,
				Computed: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Description: "Extra specifications of the volume type visible to the current role. " +
					"Administrative specifications that the API does not return are not added to the state.",
			},
			"supports_custom_iops": {
				Type:     schema.TypeBool,
				Computed: true,
				Description: "Whether the volume type supports custom IOPS. " +
					"To set custom IOPS, use the `total_iops_sec` key in the `metadata` argument of the [selectel_compute_volume_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/compute_volume_v3) resource.",
			},
		},
	}
}

func dataSourceComputeVolumeTypeV3Read(
	ctx context.Context,
	d *schema.ResourceData,
	meta any,
) diag.Diagnostics {
	client, diagnostics := getBlockStorageClient(d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	var selected *volumetype.View

	if volumeTypeID, ok := d.GetOk("volume_type_id"); ok {
		volumeTypeID := volumeTypeID.(string)
		var err error

		selected, _, err = volumetype.Get(ctx, client, volumeTypeID)
		if err != nil {
			if blockstorage.IsKind(err, blockstorage.KindNotFound) {
				return diag.Errorf(
					"Block Storage volume type %q does not exist or is not accessible in the selected project and region; "+
						"check volume_type_id, project_id, region, and access permissions: %v",
					volumeTypeID,
					err,
				)
			}

			return blockStorageOperationDiagnostics("read the volume type by ID", err)
		}
	} else {
		name := d.Get("name").(string)
		volumeTypes, err := volumetype.List(ctx, client, volumetype.ListOpts{})
		if err != nil {
			return blockStorageOperationDiagnostics("read the complete volume type list", err)
		}

		matches := make([]*volumetype.View, 0, 1)
		for i := range volumeTypes {
			if volumeTypes[i].Name == name {
				matches = append(matches, &volumeTypes[i])
			}
		}

		switch len(matches) {
		case 0:
			return diag.Errorf(
				"Block Storage volume type %q was not found: the name is unknown, or it is a regional "+
					"volume type name that can only be used when creating a volume together with an availability zone",
				name,
			)
		case 1:
			selected = matches[0]
		default:
			return diag.Errorf(
				"found %d Block Storage volume types named %q; use volume_type_id for an unambiguous lookup",
				len(matches),
				name,
			)
		}
	}

	qosLimits, _, err := volumetype.ListQoSLimits(ctx, client)
	if err != nil {
		return blockStorageOperationDiagnostics("read volume type QoS capabilities", err)
	}

	if err := setComputeVolumeTypeV3(d, selected, supportsCustomIOPS(selected.ID, qosLimits)); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

func setComputeVolumeTypeV3(
	d *schema.ResourceData,
	selected *volumetype.View,
	supportsCustomIOPS bool,
) error {
	if err := d.Set("name", selected.Name); err != nil {
		return fmt.Errorf("failed to set Block Storage volume type name: %w", err)
	}
	if err := d.Set("description", selected.Description); err != nil {
		return fmt.Errorf("failed to set Block Storage volume type description: %w", err)
	}
	if err := d.Set("is_public", selected.IsPublic); err != nil {
		return fmt.Errorf("failed to set Block Storage volume type visibility: %w", err)
	}
	if err := d.Set("extra_specs", selected.ExtraSpecs); err != nil {
		return fmt.Errorf("failed to set Block Storage volume type extra_specs: %w", err)
	}
	if err := d.Set("supports_custom_iops", supportsCustomIOPS); err != nil {
		return fmt.Errorf("failed to set Block Storage volume type custom IOPS capability: %w", err)
	}

	d.SetId(selected.ID)

	return nil
}

func supportsCustomIOPS(volumeTypeID string, limits []volumetype.QoSLimitsView) bool {
	for i := range limits {
		if limits[i].VolumeTypeID == volumeTypeID && limits[i].AllowUserQoS && limits[i].FullQoSDiskType {
			return true
		}
	}

	return false
}
