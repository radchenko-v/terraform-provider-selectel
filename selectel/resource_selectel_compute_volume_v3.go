package selectel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	blockstorage "github.com/selectel/blockstorage-go/pkg/v1"
	"github.com/selectel/blockstorage-go/pkg/v1/volume"
)

const (
	computeVolumeV3ReservedMetadataKey        = "selectel_tf_create_token"
	computeVolumeV3UserIOPSMetadataKey        = "total_iops_sec"
	computeVolumeV3ServerBandwidthMetadataKey = "total_bytes_sec"
	computeVolumeV3PendingIDPrefix            = "pending:"
	computeVolumeV3PollInterval               = 3 * time.Second

	computeVolumeV3AttachmentDescription = "Each attachment contains the `id`, `instance_id`, and `device` attributes. " +
		"The `id` attribute is the network volume ID that the Block Storage API repeats in each attachment record, not an attachment ID. " +
		"The `instance_id` attribute is the unique identifier of the cloud server. " +
		"The `device` attribute is the device name on the cloud server."

	computeVolumeV3SourceImportNote = "The value is not restored during import: " +
		"adding it to the configuration of an imported network volume replaces the network volume."
)

var computeVolumeV3Docs = resourceDocs{Name: "network volume"}

func resourceComputeVolumeV3() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceComputeVolumeV3Create,
		ReadContext:   resourceComputeVolumeV3Read,
		UpdateContext: resourceComputeVolumeV3Update,
		DeleteContext: resourceComputeVolumeV3Delete,
		Importer: &schema.ResourceImporter{
			StateContext: resourceComputeVolumeV3ImportState,
		},
		Identity: &schema.ResourceIdentity{
			SchemaFunc: resourceComputeVolumeV3IdentitySchema,
		},
		Description: "Creates and manages a network volume in Cloud Servers using the v3 public API. " +
			"For more information about network volumes, see the " +
			"[official Selectel documentation](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/).\n\n" +
			"The default `timeouts` are `10m` for `create` and `delete`, and `20m` for `update`, which waits for a network volume resize to complete.",
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(20 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
		Schema:        resourceComputeVolumeV3Schema(),
		CustomizeDiff: customizeComputeVolumeV3Diff,
	}
}

func resourceComputeVolumeV3Create(
	ctx context.Context,
	d *schema.ResourceData,
	meta any,
) diag.Diagnostics {
	client, diagnostics := getBlockStorageClient(d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	return createComputeVolumeV3(ctx, d, client)
}

func resourceComputeVolumeV3Read(
	ctx context.Context,
	d *schema.ResourceData,
	meta any,
) diag.Diagnostics {
	client, diagnostics := getBlockStorageClient(d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	return readComputeVolumeV3(ctx, d, client)
}

func resourceComputeVolumeV3Update(
	ctx context.Context,
	d *schema.ResourceData,
	meta any,
) diag.Diagnostics {
	client, diagnostics := getBlockStorageClient(d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	return updateComputeVolumeV3(ctx, d, client)
}

func resourceComputeVolumeV3Delete(
	ctx context.Context,
	d *schema.ResourceData,
	meta any,
) diag.Diagnostics {
	client, diagnostics := getBlockStorageClient(d, meta)
	if diagnostics.HasError() {
		return diagnostics
	}

	return deleteComputeVolumeV3(ctx, d, client)
}

func resourceComputeVolumeV3Schema() map[string]*schema.Schema {
	return computeVolumeV3Docs.withDocsHints(map[string]*schema.Schema{
		"id":         computeVolumeV3Docs.idResourceSchema(),
		"project_id": withComputeVolumeV3NotWhiteSpace(projectIDResourceSchema()),
		"region":     withComputeVolumeV3NotWhiteSpace(computeVolumeV3Docs.regionResourceSchema()),
		"size": {
			Type:         schema.TypeInt,
			Required:     true,
			ValidateFunc: validation.IntAtLeast(1),
			Description: "Network volume size in GB. Must be at least `1`. " +
				"When `snapshot_id`, `source_vol_id`, `image_id`, or `backup_id` is set, the size must be equal to or greater than the source size. " +
				"The maximum size depends on the network volume type, pool segment, and whether the network volume is used as a boot or additional volume. " +
				"Learn more about maximum sizes in the [Network volume limits](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-limits). " +
				"You can only increase the size. To increase the size of an attached network volume, set `enable_online_resize` to `true`. " +
				"Learn more about [increasing a network volume](https://docs.selectel.ru/en/cloud-servers/volumes/edit-volume/#enlarge-volume).",
		},
		"enable_online_resize": {
			Type:     schema.TypeBool,
			Optional: true,
			Default:  false,
			Description: "Specifies if Terraform can increase the size of an attached network volume. " +
				"If `false`, Terraform returns an error when you increase the size of an attached network volume. Detach the network volume before retrying. " +
				"If `true`, Terraform requests an [online resize](https://docs.selectel.ru/en/cloud-servers/volumes/edit-volume/#enlarge-volume) while the network volume remains attached. " +
				"The setting is stored only in the Terraform state and is set to `false` after import.",
		},
		"name": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Network volume name.",
		},
		"description": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Network volume description.",
		},
		"availability_zone": {
			// The API selects a zone when none is configured.
			Type:     schema.TypeString,
			Optional: true,
			Computed: true,
			ForceNew: true,
			Description: "Pool segment where the network volume is located, for example, `ru-1a`. " +
				"If omitted, the Block Storage API selects a pool segment. " +
				"Learn more about available pool segments in the [Availability matrix](https://docs.selectel.ru/en/infrastructure/availability-matrix/#network-volumes).",
		},
		"metadata": {
			// The API adds service metadata, including server-managed total_bytes_sec.
			Type:             schema.TypeMap,
			Optional:         true,
			Computed:         true,
			Elem:             &schema.Schema{Type: schema.TypeString},
			ValidateDiagFunc: validateComputeVolumeV3Metadata,
			Description: "Key-value pairs associated with the network volume. " +
				"Metadata keys and values must contain at most 255 characters each. " +
				"The `total_iops_sec` key configures total read and write IOPS for a Universal v2 or SSD Fast v2 network volume: " +
				"available values are from `2000` to `16000` for Universal v2 and from `25000` to `75000` for SSD Fast v2. " +
				"The default values are `2000` and `25000`, respectively. " +
				"The Block Storage API does not allow removing `total_iops_sec` after network volume creation: " +
				"if you omit an existing value, the provider preserves it. To change the value, set another supported value. " +
				"The `total_bytes_sec` key is managed by the Block Storage API: " +
				"after the API returns this key, the provider preserves its value and ignores attempts to change or remove it from the configuration. " +
				"The `selectel_tf_create_token` key is reserved by the provider, cannot be configured, and is not added to the state. " +
				"The API can add other service metadata, which the provider publishes in the state. " +
				"Learn more about available values in the [Network volume limits](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-limits).",
		},
		"volume_type": {
			// The API selects the default type when none is configured.
			Type:             schema.TypeString,
			Optional:         true,
			Computed:         true,
			ForceNew:         true,
			DiffSuppressFunc: suppressEquivalentComputeVolumeV3Type,
			Description: "Network volume type, for example, `fast.ru-1a`. " +
				"Available type prefixes are `basic`, `basicssd`, `universal`, `universal2`, `fast`, and `fast2`. " +
				"The format is `<volume_type>.<pool_segment>`. " +
				"We recommend specifying the type that matches `availability_zone`. " +
				"If the API replaces a pool suffix with the network volume's pool segment, the provider treats the returned name as the same type. " +
				"If omitted, the Block Storage API selects the default type: the project default when configured, otherwise the platform default. " +
				"To retrieve the current default type, set `volume_type_id = \"default\"` in the " +
				"[selectel_compute_volume_type_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/compute_volume_type_v3) data source. " +
				"Learn more about available types in the [Network volume types list](https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-types-list).",
		},
		// Cinder validates source references; imageRef also accepts image names and URLs.
		"snapshot_id": {
			Type:          schema.TypeString,
			Optional:      true,
			ForceNew:      true,
			ConflictsWith: []string{"source_vol_id", "image_id", "backup_id"},
			Description: "Unique identifier of a snapshot to use as the network volume source. " +
				"Conflicts with `source_vol_id`, `image_id`, and `backup_id`. " +
				"You can retrieve the ID with the [selectel_compute_snapshot_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/compute_snapshot_v3) data source. " +
				computeVolumeV3SourceImportNote + " " +
				"Learn more about [snapshots](https://docs.selectel.ru/en/cloud-servers/volumes/snapshots/).",
		},
		"source_vol_id": {
			Type:          schema.TypeString,
			Optional:      true,
			ForceNew:      true,
			ConflictsWith: []string{"snapshot_id", "image_id", "backup_id"},
			Description: "Unique identifier of another network volume to copy. " +
				"Conflicts with `snapshot_id`, `image_id`, and `backup_id`. " +
				"You can retrieve the ID with the [selectel_compute_volume_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/compute_volume_v3) data source " +
				"or from the [selectel_compute_volume_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/compute_volume_v3) resource. " +
				computeVolumeV3SourceImportNote,
		},
		"image_id": {
			Type:          schema.TypeString,
			Optional:      true,
			ForceNew:      true,
			ConflictsWith: []string{"snapshot_id", "source_vol_id", "backup_id"},
			Description: "Unique identifier of an image to use as the network volume source. " +
				"Conflicts with `snapshot_id`, `source_vol_id`, and `backup_id`. " +
				"Learn more about the [openstack_images_image_v2](https://registry.terraform.io/providers/terraform-provider-openstack/openstack/latest/docs/data-sources/images_image_v2) data source in the official OpenStack documentation. " +
				computeVolumeV3SourceImportNote,
		},
		"backup_id": {
			Type:          schema.TypeString,
			Optional:      true,
			ForceNew:      true,
			ConflictsWith: []string{"snapshot_id", "source_vol_id", "image_id"},
			Description: "Unique identifier of a backup to restore. " +
				"Conflicts with `snapshot_id`, `source_vol_id`, and `image_id`. " +
				"You can retrieve the ID from the `checkpoints[*].list[*].checkpoint_items[*].backup_id` attribute of the " +
				"[selectel_cloudbackup_checkpoint_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/cloudbackup_checkpoint_v2) data source. " +
				computeVolumeV3SourceImportNote + " " +
				"Learn more about [backups](https://docs.selectel.ru/en/cloud-servers/backups/about-backups/).",
		},
		"attachment": {
			Type:     schema.TypeSet,
			Computed: true,
			Description: "Network volume attachments. The provider does not manage attachments: " +
				"[attach or detach the network volume](https://docs.selectel.ru/en/cloud-servers/volumes/attach-detach-volume/) in the Control panel or with OpenStack CLI. " +
				"Detach the network volume before destroying it. " +
				"If the network volume is attached, Terraform returns an error and keeps it in the state. " +
				computeVolumeV3AttachmentDescription,
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
		},
	})
}

func withComputeVolumeV3NotWhiteSpace(attribute *schema.Schema) *schema.Schema {
	attribute.ValidateFunc = validation.StringIsNotWhiteSpace

	return attribute
}

func resourceComputeVolumeV3IdentitySchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"id": computeVolumeV3Docs.idIdentitySchema(
			"You can retrieve the ID with the [selectel_compute_volume_v3](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/compute_volume_v3) data source. " +
				"You can also use the `openstack --os-region-name <region> volume list` command. " +
				"Learn more about [OpenStack CLI](https://docs.selectel.ru/en/cloud-servers/tools/openstack-cli/)."),
		"project_id": projectIDIdentitySchema(),
		"region": computeVolumeV3Docs.regionIdentitySchema(
			"This is the pool of the network volume, not the pool used for API authorization."),
	}
}

func customizeComputeVolumeV3Diff(
	_ context.Context,
	d *schema.ResourceDiff,
	_ any,
) error {
	if d.Id() != "" && d.NewValueKnown("size") {
		oldSize, newSize := d.GetChange("size")
		if newSize.(int) < oldSize.(int) {
			// A replacement creates a different volume and may use a smaller size.
			// Use actual diff keys so a suppressed volume type alias is not a replacement.
			volumeSchema := resourceComputeVolumeV3Schema()
			for _, key := range d.GetChangedKeysPrefix("") {
				if attribute, exists := volumeSchema[key]; exists && attribute.ForceNew {
					return nil
				}
			}

			return fmt.Errorf("cannot shrink Block Storage volume %s from %d GiB to %d GiB", d.Id(), oldSize, newSize)
		}
	}

	// Cinder does not allow removing existing QoS metadata.
	// Keep omitted IOPS and server-managed bandwidth so plans converge.
	oldRaw, newRaw := d.GetChange("metadata")
	oldMetadata, oldOK := oldRaw.(map[string]any)
	newMetadata, newOK := newRaw.(map[string]any)
	if !oldOK || !newOK {
		return nil
	}

	mergedMetadata := make(map[string]any, len(newMetadata)+2)
	maps.Copy(mergedMetadata, newMetadata)
	changed := false

	if currentIOPS, exists := oldMetadata[computeVolumeV3UserIOPSMetadataKey]; exists && currentIOPS != nil {
		if _, planned := newMetadata[computeVolumeV3UserIOPSMetadataKey]; !planned {
			mergedMetadata[computeVolumeV3UserIOPSMetadataKey] = currentIOPS
			changed = true
		}
	}

	if serverBandwidth, exists := oldMetadata[computeVolumeV3ServerBandwidthMetadataKey]; exists && serverBandwidth != nil {
		if plannedBandwidth, planned := newMetadata[computeVolumeV3ServerBandwidthMetadataKey]; !planned || plannedBandwidth != serverBandwidth {
			mergedMetadata[computeVolumeV3ServerBandwidthMetadataKey] = serverBandwidth
			changed = true
		}
	}

	if !changed {
		return nil
	}

	return d.SetNew("metadata", mergedMetadata)
}

func validateComputeVolumeV3Metadata(value any, path cty.Path) diag.Diagnostics {
	metadata := value.(map[string]any)
	if _, exists := metadata[computeVolumeV3ReservedMetadataKey]; !exists {
		return nil
	}

	return diag.Diagnostics{{
		Severity: diag.Error,
		Summary:  "Reserved Block Storage volume metadata key",
		Detail: fmt.Sprintf(
			"metadata key %q is reserved by the provider for safe volume creation recovery",
			computeVolumeV3ReservedMetadataKey,
		),
		AttributePath: append(
			path,
			cty.IndexStep{Key: cty.StringVal(computeVolumeV3ReservedMetadataKey)},
		),
	}}
}

func suppressEquivalentComputeVolumeV3Type(
	_ string,
	stateValue string,
	configValue string,
	d *schema.ResourceData,
) bool {
	region := d.Get("region").(string)
	availabilityZone := d.Get("availability_zone").(string)

	if len(availabilityZone) != len(region)+1 || !strings.HasPrefix(availabilityZone, region) {
		return false
	}

	regionSuffix := "." + region
	availabilityZoneSuffix := "." + availabilityZone
	if !strings.HasSuffix(configValue, regionSuffix) || !strings.HasSuffix(stateValue, availabilityZoneSuffix) {
		return false
	}

	configPrefix := strings.TrimSuffix(configValue, regionSuffix)
	statePrefix := strings.TrimSuffix(stateValue, availabilityZoneSuffix)

	return configPrefix != "" && configPrefix == statePrefix
}

func resourceComputeVolumeV3ImportState(
	_ context.Context,
	d *schema.ResourceData,
	meta any,
) ([]*schema.ResourceData, error) {
	// Import by identity: Terraform 1.12+ passes id, project_id and region in the identity block.
	if d.Id() == "" {
		identity, err := d.Identity()
		if err != nil {
			return nil, fmt.Errorf("failed to get Block Storage volume identity: %w", err)
		}

		d.SetId(identity.Get("id").(string))
		for _, key := range []string{"project_id", "region"} {
			if err := d.Set(key, identity.Get(key)); err != nil {
				return nil, fmt.Errorf("failed to set %s during Block Storage volume import: %w", key, err)
			}
		}

		return []*schema.ResourceData{d}, nil
	}

	// Import by string ID: project and region come from the provider configuration.
	config := meta.(*Config)
	if config.ProjectID == "" {
		return nil, fmt.Errorf("INFRA_PROJECT_ID must be set for the Block Storage volume import")
	}
	if config.Region == "" {
		return nil, fmt.Errorf("INFRA_REGION must be set for the Block Storage volume import")
	}

	if err := d.Set("project_id", config.ProjectID); err != nil {
		return nil, fmt.Errorf("failed to set project_id during Block Storage volume import: %w", err)
	}
	if err := d.Set("region", config.Region); err != nil {
		return nil, fmt.Errorf("failed to set region during Block Storage volume import: %w", err)
	}

	return []*schema.ResourceData{d}, nil
}

func createComputeVolumeV3(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
) diag.Diagnostics {
	createOpts := expandComputeVolumeV3CreateOpts(d)

	createToken, err := uuid.GenerateUUID()
	if err != nil {
		return diag.Errorf("failed to generate the Block Storage volume create token: %v", err)
	}

	pendingID := computeVolumeV3PendingIDPrefix + createToken
	d.SetId(pendingID)
	createOpts.Metadata[computeVolumeV3ReservedMetadataKey] = createToken

	created, _, createErr := volume.Create(ctx, client, createOpts)
	if createErr == nil && (created == nil || created.ID == "") {
		createErr = errors.New("block storage accepted the create request without returning a volume ID")
	}

	if createErr != nil {
		if isDefinitiveComputeVolumeV3CreateFailure(createErr) {
			d.SetId("")

			return diag.Errorf("Block Storage rejected the volume creation before it was queued: %v", createErr)
		}

		created, err = recoverComputeVolumeV3Create(
			ctx,
			client,
			createToken,
			d.Timeout(schema.TimeoutCreate),
		)
		if err != nil {
			return ambiguousComputeVolumeV3CreateDiagnostics(pendingID, createErr, err)
		}
	}

	d.SetId(created.ID)

	err = waitForComputeVolumeV3Available(
		ctx,
		client,
		created.ID,
		d.Timeout(schema.TimeoutCreate),
	)
	if err != nil {
		return diag.Errorf(
			"failed waiting for Block Storage volume %s to become available; "+
				"the volume ID remains in Terraform state: %v",
			created.ID,
			err,
		)
	}

	return readComputeVolumeV3(ctx, d, client)
}

func readComputeVolumeV3(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
) diag.Diagnostics {
	volumeID := d.Id()
	if strings.HasPrefix(volumeID, computeVolumeV3PendingIDPrefix) {
		if diagnostics := recoverPendingComputeVolumeV3(ctx, d, client); diagnostics.HasError() {
			return diagnostics
		}

		volumeID = d.Id()

		if err := waitForComputeVolumeV3Available(
			ctx,
			client,
			volumeID,
			d.Timeout(schema.TimeoutCreate),
		); err != nil {
			if blockstorage.IsKind(err, blockstorage.KindNotFound) {
				return confirmComputeVolumeV3NotFound(ctx, d, client, volumeID, err)
			}

			return diag.Errorf(
				"failed waiting for recovered Block Storage volume %s to become available; "+
					"the volume ID remains in Terraform state: %v",
				volumeID,
				err,
			)
		}
	}

	current, _, err := volume.Get(ctx, client, volumeID)
	if err != nil {
		if blockstorage.IsKind(err, blockstorage.KindNotFound) {
			return confirmComputeVolumeV3NotFound(ctx, d, client, volumeID, err)
		}

		return blockStorageOperationDiagnostics(
			fmt.Sprintf("read Block Storage volume %s", volumeID),
			err,
		)
	}

	if err := setComputeVolumeV3(d, current); err != nil {
		return diag.FromErr(err)
	}

	if identity, err := d.Identity(); err == nil {
		_ = identity.Set("id", d.Id())
		_ = identity.Set("project_id", d.Get("project_id"))
		_ = identity.Set("region", d.Get("region"))
	}

	return nil
}

func recoverPendingComputeVolumeV3(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
) diag.Diagnostics {
	pendingID := d.Id()
	if !strings.HasPrefix(pendingID, computeVolumeV3PendingIDPrefix) {
		return nil
	}

	createToken := strings.TrimPrefix(pendingID, computeVolumeV3PendingIDPrefix)
	if createToken == "" {
		return diag.Errorf(
			"Block Storage volume has invalid temporary ID %q; inspect the project volumes "+
				"and import the matching volume manually",
			pendingID,
		)
	}

	volumes, err := volume.ListDetail(ctx, client, volume.ListOpts{})
	if err != nil {
		return blockStorageOperationDiagnostics(
			fmt.Sprintf("recover Block Storage volume with temporary ID %q", pendingID),
			err,
		)
	}

	matches := make([]string, 0, 1)
	for _, candidate := range volumes {
		if candidate.Metadata[computeVolumeV3ReservedMetadataKey] == createToken {
			matches = append(matches, candidate.ID)
		}
	}

	switch len(matches) {
	case 0:
		return diag.Errorf(
			"Block Storage volume recovery found no volume for temporary ID %q. "+
				"Terraform kept the temporary ID and will not repeat POST Create automatically. "+
				"Re-run Terraform to retry the search, or inspect the project volumes by metadata key %q",
			pendingID,
			computeVolumeV3ReservedMetadataKey,
		)
	case 1:
		if matches[0] == "" {
			return diag.Errorf(
				"Block Storage volume recovery returned no volume ID for temporary ID %q. "+
					"Terraform kept the temporary ID and will not repeat POST Create automatically",
				pendingID,
			)
		}

		d.SetId(matches[0])

		return nil
	default:
		return diag.Errorf(
			"Block Storage volume recovery found %d volumes for temporary ID %q: %s. "+
				"Terraform kept the temporary ID and will not choose a volume or repeat POST Create automatically",
			len(matches),
			pendingID,
			strings.Join(matches, ", "),
		)
	}
}

func confirmComputeVolumeV3NotFound(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
	volumeID string,
	getErr error,
) diag.Diagnostics {
	absent, err := isComputeVolumeV3Absent(ctx, client, volumeID)
	if err != nil {
		return blockStorageOperationDiagnostics(
			fmt.Sprintf(
				"confirm that Block Storage volume %s is absent after a NotFound response",
				volumeID,
			),
			err,
		)
	}

	if !absent {
		return diag.Errorf(
			"Block Storage returned NotFound while reading volume %s, but the complete volume list "+
				"still contains that ID. Terraform kept the resource state because the direct response "+
				"indicates a masked authorization failure: %v",
			volumeID,
			getErr,
		)
	}

	d.SetId("")

	return nil
}

func isComputeVolumeV3Absent(
	ctx context.Context,
	client *blockstorage.Client,
	volumeID string,
) (bool, error) {
	volumes, err := volume.ListDetail(ctx, client, volume.ListOpts{})
	if err != nil {
		return false, err
	}

	for _, candidate := range volumes {
		if candidate.ID == volumeID {
			return false, nil
		}
	}

	return true, nil
}

func setComputeVolumeV3(d *schema.ResourceData, current *volume.View) error {
	metadata := make(map[string]string, len(current.Metadata))
	for key, value := range current.Metadata {
		if key != computeVolumeV3ReservedMetadataKey {
			metadata[key] = value
		}
	}

	attachments := make([]map[string]any, 0, len(current.Attachments))
	for _, attachment := range current.Attachments {
		attachments = append(attachments, map[string]any{
			"id":          attachment.ID,
			"instance_id": attachment.ServerID,
			"device":      attachment.Device,
		})
	}

	// Source fields are creation intent. Leave them untouched so configured values remain in state
	// without importing Cinder provenance into ForceNew fields.
	values := []struct {
		name  string
		value any
	}{
		{name: "size", value: current.Size},
		{name: "name", value: current.Name},
		{name: "description", value: current.Description},
		{name: "availability_zone", value: current.AvailabilityZone},
		{name: "metadata", value: metadata},
		{name: "volume_type", value: current.VolumeType},
		{name: "attachment", value: attachments},
	}

	for _, field := range values {
		if err := d.Set(field.name, field.value); err != nil {
			return fmt.Errorf(
				"failed to set Block Storage volume %s in Terraform state: %w",
				field.name,
				err,
			)
		}
	}

	return nil
}

func deleteComputeVolumeV3(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
) diag.Diagnostics {
	if diagnostics := recoverPendingComputeVolumeV3(ctx, d, client); diagnostics.HasError() {
		return diagnostics
	}

	volumeID := d.Id()
	current, _, err := volume.Get(ctx, client, volumeID)
	if err != nil {
		if blockstorage.IsKind(err, blockstorage.KindNotFound) {
			return confirmComputeVolumeV3NotFound(ctx, d, client, volumeID, err)
		}

		return blockStorageOperationDiagnostics(
			fmt.Sprintf("check Block Storage volume %s before deleting it", volumeID),
			err,
		)
	}

	if len(current.Attachments) != 0 {
		return diag.Errorf(
			"cannot delete attached Block Storage volume %s; detach it from all instances first. "+
				"Terraform kept the volume ID in state",
			volumeID,
		)
	}

	if current.Status != "deleting" {
		_, err = volume.Delete(ctx, client, volumeID)
		if err != nil {
			if blockstorage.IsKind(err, blockstorage.KindNotFound) {
				return confirmComputeVolumeV3NotFound(ctx, d, client, volumeID, err)
			}

			return blockStorageOperationDiagnostics(
				fmt.Sprintf("delete Block Storage volume %s", volumeID),
				err,
			)
		}
	}

	if err := waitForComputeVolumeV3Deleted(
		ctx,
		client,
		volumeID,
		d.Timeout(schema.TimeoutDelete),
	); err != nil {
		return diag.Errorf(
			"failed waiting for Block Storage volume %s to be deleted; "+
				"the volume ID remains in Terraform state: %v",
			volumeID,
			err,
		)
	}

	d.SetId("")

	return nil
}

type computeVolumeV3UpdatePhase struct {
	name string
	run  func() error
}

type computeVolumeV3ResizePlan struct {
	needsExtend bool
	needsWait   bool
	attached    bool
}

func updateComputeVolumeV3(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
) diag.Diagnostics {
	if diagnostics := recoverPendingComputeVolumeV3(ctx, d, client); diagnostics.HasError() {
		return diagnostics
	}

	volumeID := d.Id()

	oldSizeRaw, newSizeRaw := d.GetChange("size")
	oldSize := oldSizeRaw.(int)
	newSize := newSizeRaw.(int)
	sizeChanged := d.HasChange("size")
	if sizeChanged && newSize < oldSize {
		return diag.Errorf(
			"cannot shrink Block Storage volume %s from %d GiB to %d GiB",
			volumeID,
			oldSize,
			newSize,
		)
	}

	resizePlan := computeVolumeV3ResizePlan{}
	if sizeChanged && newSize > oldSize {
		var diagnostics diag.Diagnostics
		resizePlan, diagnostics = planComputeVolumeV3Resize(ctx, d, client, newSize)
		if diagnostics.HasError() {
			return diagnostics
		}
		if d.Id() == "" {
			return diag.Errorf(
				"Block Storage volume %s was removed outside Terraform; no updates were applied. Run terraform plan again",
				volumeID,
			)
		}
	}

	phases := make([]computeVolumeV3UpdatePhase, 0, 3)
	if d.HasChanges("name", "description", "metadata") {
		updateOpts := volume.UpdateOpts{}
		if d.HasChange("name") {
			name := d.Get("name").(string)
			updateOpts.Name = &name
		}
		if d.HasChange("description") {
			description := d.Get("description").(string)
			updateOpts.Description = &description
		}
		if d.HasChange("metadata") {
			updateOpts.Metadata = expandComputeVolumeV3StringMap(d.Get("metadata"))
		}

		phases = append(phases, computeVolumeV3UpdatePhase{
			name: "descriptive update",
			run: func() error {
				_, _, err := volume.Update(
					ctx,
					client,
					volumeID,
					updateOpts,
				)

				return err
			},
		})
	}
	if resizePlan.needsExtend {
		phases = append(phases,
			computeVolumeV3UpdatePhase{
				name: "size extend request",
				run: func() error {
					_, err := volume.Extend(
						ctx,
						client,
						volumeID,
						volume.ExtendOpts{
							NewSize:  newSize,
							Attached: resizePlan.attached,
						},
					)

					return err
				},
			},
		)
	}
	if resizePlan.needsExtend || resizePlan.needsWait {
		phases = append(phases, computeVolumeV3UpdatePhase{
			name: "size wait",
			run: func() error {
				return waitForComputeVolumeV3Resize(
					ctx,
					client,
					volumeID,
					d.Timeout(schema.TimeoutUpdate),
				)
			},
		})
	}

	phaseNames := make([]string, 0, len(phases))
	for _, phase := range phases {
		phaseNames = append(phaseNames, phase.name)
	}

	for i, phase := range phases {
		if err := phase.run(); err != nil {
			return failedComputeVolumeV3UpdateDiagnostics(
				ctx,
				d,
				client,
				phaseNames[:i],
				phaseNames[i:],
				err,
			)
		}
	}

	return readComputeVolumeV3(ctx, d, client)
}

func planComputeVolumeV3Resize(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
	newSize int,
) (computeVolumeV3ResizePlan, diag.Diagnostics) {
	volumeID := d.Id()
	current, _, err := volume.Get(ctx, client, volumeID)
	if err != nil {
		if blockstorage.IsKind(err, blockstorage.KindNotFound) {
			return computeVolumeV3ResizePlan{},
				confirmComputeVolumeV3NotFound(ctx, d, client, volumeID, err)
		}

		return computeVolumeV3ResizePlan{}, blockStorageOperationDiagnostics(
			fmt.Sprintf("check Block Storage volume %s before resizing", volumeID),
			err,
		)
	}

	if newSize < current.Size {
		if err := setComputeVolumeV3(d, current); err != nil {
			return computeVolumeV3ResizePlan{}, diag.FromErr(err)
		}

		return computeVolumeV3ResizePlan{}, diag.Errorf(
			"cannot shrink Block Storage volume %s from its remote size %d GiB to %d GiB",
			volumeID,
			current.Size,
			newSize,
		)
	}

	if current.Status == "extending" {
		return computeVolumeV3ResizePlan{needsWait: true}, nil
	}
	if newSize == current.Size {
		return computeVolumeV3ResizePlan{}, nil
	}

	plan := computeVolumeV3ResizePlan{
		needsExtend: true,
		attached:    current.Status == "in-use",
	}
	if plan.attached && !d.Get("enable_online_resize").(bool) {
		if err := setComputeVolumeV3(d, current); err != nil {
			return computeVolumeV3ResizePlan{}, diag.FromErr(err)
		}

		return computeVolumeV3ResizePlan{}, diag.Errorf(
			"cannot resize attached Block Storage volume %s while enable_online_resize is false",
			volumeID,
		)
	}

	return plan, nil
}

func failedComputeVolumeV3UpdateDiagnostics(
	ctx context.Context,
	d *schema.ResourceData,
	client *blockstorage.Client,
	completed []string,
	unfinished []string,
	updateErr error,
) diag.Diagnostics {
	volumeID := d.Id()
	refreshDiagnostics := readComputeVolumeV3(ctx, d, client)
	if d.Id() == "" {
		d.SetId(volumeID)
	}

	completedText := "none"
	if len(completed) != 0 {
		completedText = strings.Join(completed, ", ")
	}

	refreshText := "Terraform refreshed the remote volume state after the failure"
	if refreshDiagnostics.HasError() {
		refreshText = "Terraform could not refresh the remote volume state after the failure: " +
			refreshDiagnostics[0].Summary
	}

	return diag.Errorf(
		"failed to update Block Storage volume %s during phase %q: %v. "+
			"Completed phases: %s. Unfinished phases: %s. %s",
		volumeID,
		unfinished[0],
		updateErr,
		completedText,
		strings.Join(unfinished, ", "),
		refreshText,
	)
}

func waitForComputeVolumeV3Resize(
	ctx context.Context,
	client *blockstorage.Client,
	volumeID string,
	timeout time.Duration,
) error {
	var lastStatus string

	stateConf := &resource.StateChangeConf{
		Pending: []string{"extending"},
		Target:  []string{"available", "in-use"},
		Refresh: func() (any, string, error) {
			current, _, err := volume.Get(ctx, client, volumeID)
			if err != nil {
				return nil, "", err
			}

			lastStatus = current.Status

			return current, current.Status, nil
		},
		Timeout:    timeout,
		MinTimeout: computeVolumeV3PollInterval,
	}

	_, err := stateConf.WaitForStateContext(ctx)
	if err != nil {
		if lastStatus != "" {
			return fmt.Errorf("%w; last observed status: %s", err, lastStatus)
		}
	}

	return err
}

func waitForComputeVolumeV3Deleted(
	ctx context.Context,
	client *blockstorage.Client,
	volumeID string,
	timeout time.Duration,
) error {
	var lastStatus string

	stateConf := &resource.StateChangeConf{
		Pending: []string{"available", "downloading", "deleting"},
		Target:  []string{"deleted"},
		Refresh: func() (any, string, error) {
			current, _, err := volume.Get(ctx, client, volumeID)
			if err != nil {
				if !blockstorage.IsKind(err, blockstorage.KindNotFound) {
					return nil, "", err
				}

				absent, confirmErr := isComputeVolumeV3Absent(ctx, client, volumeID)
				if confirmErr != nil {
					return nil, "", fmt.Errorf(
						"failed to confirm that Block Storage volume %s is absent: %w",
						volumeID,
						confirmErr,
					)
				}
				if !absent {
					return nil, "", fmt.Errorf(
						"block storage returned NotFound while waiting for volume %s to be deleted, "+
							"but the complete volume list still contains that ID; "+
							"the direct response indicates a masked authorization failure: %w",
						volumeID,
						err,
					)
				}

				return volumeID, "deleted", nil
			}

			lastStatus = current.Status

			return current, current.Status, nil
		},
		Timeout:    timeout,
		MinTimeout: computeVolumeV3PollInterval,
	}

	_, err := stateConf.WaitForStateContext(ctx)
	if err != nil {
		if lastStatus != "" {
			return fmt.Errorf("%w; last observed status: %s", err, lastStatus)
		}
	}

	return err
}

func expandComputeVolumeV3CreateOpts(d *schema.ResourceData) volume.CreateOpts {
	return volume.CreateOpts{
		Size:             d.Get("size").(int),
		Name:             d.Get("name").(string),
		Description:      d.Get("description").(string),
		AvailabilityZone: d.Get("availability_zone").(string),
		Metadata:         expandComputeVolumeV3StringMap(d.Get("metadata")),
		VolumeType:       d.Get("volume_type").(string),
		SnapshotID:       d.Get("snapshot_id").(string),
		SourceVolID:      d.Get("source_vol_id").(string),
		ImageID:          d.Get("image_id").(string),
		BackupID:         d.Get("backup_id").(string),
	}
}

func expandComputeVolumeV3StringMap(raw any) map[string]string {
	values := raw.(map[string]any)

	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value.(string)
	}

	return result
}

func isDefinitiveComputeVolumeV3CreateFailure(err error) bool {
	var sdkErr *blockstorage.Error
	if !errors.As(err, &sdkErr) ||
		sdkErr.RequestID == "" ||
		!sdkErr.StructuredFault {
		return false
	}

	switch sdkErr.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return sdkErr.Kind == blockstorage.KindInvalidRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		return sdkErr.Kind == blockstorage.KindForbidden
	case http.StatusNotFound:
		return sdkErr.Kind == blockstorage.KindNotFound
	case http.StatusNotAcceptable:
		return sdkErr.Kind == blockstorage.KindMicroversion
	case http.StatusRequestEntityTooLarge:
		return sdkErr.Kind == blockstorage.KindOverQuota
	default:
		return false
	}
}

func recoverComputeVolumeV3Create(
	ctx context.Context,
	client *blockstorage.Client,
	createToken string,
	timeout time.Duration,
) (*volume.View, error) {
	stateConf := &resource.StateChangeConf{
		Pending: []string{"searching"},
		Target:  []string{"found"},
		Refresh: func() (any, string, error) {
			volumes, err := volume.ListDetail(ctx, client, volume.ListOpts{})
			if err != nil {
				return nil, "", fmt.Errorf(
					"failed to read the complete Block Storage volume list: %w",
					err,
				)
			}

			matches := make([]volume.View, 0, 1)
			for _, candidate := range volumes {
				if candidate.Metadata[computeVolumeV3ReservedMetadataKey] == createToken {
					matches = append(matches, candidate)
				}
			}

			switch len(matches) {
			case 0:
				return createToken, "searching", nil
			case 1:
				return &matches[0], "found", nil
			default:
				ids := make([]string, 0, len(matches))
				for _, match := range matches {
					ids = append(ids, match.ID)
				}

				return nil, "", fmt.Errorf(
					"found %d Block Storage volumes with the same create token: %s",
					len(matches),
					strings.Join(ids, ", "),
				)
			}
		},
		Timeout:    timeout,
		MinTimeout: computeVolumeV3PollInterval,
	}

	result, err := stateConf.WaitForStateContext(ctx)
	if err != nil {
		return nil, err
	}

	recovered, ok := result.(*volume.View)
	if !ok || recovered.ID == "" {
		return nil, errors.New("block storage create recovery returned no volume ID")
	}

	return recovered, nil
}

func waitForComputeVolumeV3Available(
	ctx context.Context,
	client *blockstorage.Client,
	volumeID string,
	timeout time.Duration,
) error {
	var lastStatus string

	stateConf := &resource.StateChangeConf{
		Pending: []string{"creating", "downloading", "restoring-backup"},
		Target:  []string{"available"},
		Refresh: func() (any, string, error) {
			current, _, err := volume.Get(ctx, client, volumeID)
			if err != nil {
				return nil, "", err
			}

			lastStatus = current.Status

			return current, current.Status, nil
		},
		Timeout:    timeout,
		MinTimeout: computeVolumeV3PollInterval,
	}

	_, err := stateConf.WaitForStateContext(ctx)
	if err != nil {
		if lastStatus != "" {
			return fmt.Errorf("%w; last observed status: %s", err, lastStatus)
		}
	}

	return err
}

func ambiguousComputeVolumeV3CreateDiagnostics(
	pendingID string,
	createErr error,
	recoveryErr error,
) diag.Diagnostics {
	initialFailure := fmt.Sprintf("%v", createErr)
	if blockstorage.IsKind(createErr, blockstorage.KindOverQuota) {
		initialFailure = "the project quota is exhausted and requires operator action: " + initialFailure
	}

	return diag.Errorf(
		"Block Storage volume creation has an ambiguous result. Terraform kept temporary ID %q "+
			"and will not repeat POST Create automatically. Initial result: %s. "+
			"Recovery by metadata key %q failed: %v. Re-run Terraform to continue recovery, "+
			"or inspect the project volumes by that metadata value and resolve the state manually",
		pendingID,
		initialFailure,
		computeVolumeV3ReservedMetadataKey,
		recoveryErr,
	)
}
