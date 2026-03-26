package b2

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bons/bons-ci/core/content/b2/writer"
	"github.com/bons/bons-ci/core/content/local"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type b2Store struct {
	client          *minio.Client
	cfg             config
	tenant_prefixer object_folder
	store           content.Store
}

type config struct {
	Bucket            string
	Region            string
	Prefix            string
	Tenant            string
	ManifestsPrefix   string
	BlobsPrefix       string
	Names             []string
	TouchRefresh      time.Duration
	EndpointURL       string
	AccessKeyID       string
	SecretAccessKey   string
	SessionToken      string
	UsePathStyle      bool
	UploadParallelism int
}

func NewStore(attrs map[string]string, creds *credentials.Credentials) (S3ContentStore, error) {
	store, err := local.NewStore(attrs["root"])
	if err != nil {
		return nil, err
	}

	cfg, err := GetConfig(attrs)
	if err != nil {
		return nil, err
	}

	minioClient, err := minio.New(fmt.Sprintf("https://s3.%s.backblazeb2.com/%s", cfg.Region, cfg.Bucket), &minio.Options{
		Creds:  creds,
		Secure: true,
		Region: cfg.Region,
	})

	// Ensure bucket exists
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to check bucket existence: %w", err)
	}
	if !exists {
		if err := minioClient.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{
			Region: cfg.Region,
		}); err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	obj, err := RetriveTenantPrefix(cfg)
	if err != nil {
		return nil, err
	}

	return &b2Store{
		client:          minioClient,
		cfg:             cfg,
		store:           store,
		tenant_prefixer: obj,
	}, err
}

const (
	attrBucket            = "bucket"
	attrRegion            = "region"
	attrPrefix            = "prefix"
	attrTenant            = "tenant"
	attrManifestsPrefix   = "manifests_prefix"
	attrBlobsPrefix       = "blobs_prefix"
	attrName              = "name"
	attrTouchRefresh      = "touch_refresh"
	attrEndpointURL       = "endpoint_url"
	attrAccessKeyID       = "access_key_id"
	attrSecretAccessKey   = "secret_access_key"
	attrSessionToken      = "session_token"
	attrUsePathStyle      = "use_path_style"
	attrUploadParallelism = "upload_parallelism"
	maxCopyObjectSize     = 5 * 1024 * 1024 * 1024
)

const (
	default_bucket = "bons"
	default_region = "us-east-005"

	default_manifest_prefix = "manifests/"
	default_blobs_prefix    = writer.DefaultBlobsPrefix
	default_vertices_prefix = "vertices/"
)

func GetConfig(attrs map[string]string) (config, error) {
	bucket, ok := attrs[attrBucket]
	if !ok {
		bucket, ok = os.LookupEnv("AWS_BUCKET")
		if !ok {
			bucket = default_bucket
		}
	}

	region, ok := attrs[attrRegion]
	if !ok {
		region, ok = os.LookupEnv("AWS_REGION")
		if !ok {
			region = default_region
		}
	}

	tenant, ok := attrs[attrTenant]
	if !ok {
		tenant, ok = os.LookupEnv("AWS_TENANT")
		if !ok {
			return config{}, errors.Errorf("unable to retrive user tenant id")
		}
	}
	if strings.Contains(tenant, "/") {
		return config{}, errors.Errorf("invalid tenant id %q", tenant)
	}

	prefix := attrs[attrPrefix]

	manifestsPrefix, ok := attrs[attrManifestsPrefix]
	if !ok {
		manifestsPrefix = default_manifest_prefix
	}

	blobsPrefix, ok := attrs[attrBlobsPrefix]
	if !ok {
		blobsPrefix = default_blobs_prefix
	}

	names := []string{"bons"}
	name, ok := attrs[attrName]
	if ok {
		splittedNames := strings.Split(name, ";")
		if len(splittedNames) > 0 {
			names = splittedNames
		}
	}

	touchRefresh := 24 * time.Hour

	touchRefreshStr, ok := attrs[attrTouchRefresh]
	if ok {
		touchRefreshFromUser, err := time.ParseDuration(touchRefreshStr)
		if err == nil {
			touchRefresh = touchRefreshFromUser
		}
	}

	endpointURL := attrs[attrEndpointURL]
	accessKeyID := attrs[attrAccessKeyID]
	secretAccessKey := attrs[attrSecretAccessKey]
	sessionToken := attrs[attrSessionToken]

	usePathStyle := false
	usePathStyleStr, ok := attrs[attrUsePathStyle]
	if ok {
		usePathStyleUser, err := strconv.ParseBool(usePathStyleStr)
		if err == nil {
			usePathStyle = usePathStyleUser
		}
	}

	uploadParallelism := 4
	uploadParallelismStr, ok := attrs[attrUploadParallelism]
	if ok {
		uploadParallelismInt, err := strconv.Atoi(uploadParallelismStr)
		if err != nil {
			return config{}, errors.Errorf("upload_parallelism must be a positive integer")
		}
		if uploadParallelismInt <= 0 {
			return config{}, errors.Errorf("upload_parallelism must be a positive integer")
		}
		uploadParallelism = uploadParallelismInt
	}

	return config{
		Bucket:            bucket,
		Region:            region,
		Prefix:            prefix,
		ManifestsPrefix:   manifestsPrefix,
		BlobsPrefix:       blobsPrefix,
		Names:             names,
		Tenant:            tenant,
		TouchRefresh:      touchRefresh,
		EndpointURL:       endpointURL,
		AccessKeyID:       accessKeyID,
		SecretAccessKey:   secretAccessKey,
		SessionToken:      sessionToken,
		UsePathStyle:      usePathStyle,
		UploadParallelism: uploadParallelism,
	}, nil
}

type object_folder struct {
	BlobPath func(dgst digest.Digest) string
	AsFolder func(folder ...string) string
	Trim     func(folder string) string
}

func RetriveTenantPrefix(cfg config) (object_folder, error) {
	tenant := cfg.Tenant
	if tenant == "" || strings.Contains(tenant, "/") {
		return object_folder{}, errors.Errorf("invalid tenant id %q", tenant)
	}
	return object_folder{
		BlobPath: func(dgst digest.Digest) string {
			return strings.Join([]string{
				tenant,
				cfg.BlobsPrefix,
				dgst.Algorithm().String(),
				dgst.Encoded(),
			}, "/")
		},
		AsFolder: func(folder ...string) string {
			path := append([]string{tenant}, folder...)
			return strings.Join(path, "/")
		},
		Trim: func(folder string) string {
			after, _ := strings.CutPrefix(folder, strings.Join([]string{
				tenant,
				cfg.BlobsPrefix,
			}, "/"))
			return after
		},
	}, nil
}
