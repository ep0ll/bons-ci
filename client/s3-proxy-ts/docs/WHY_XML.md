# Why XML? Can We Use JSON?

## Short answer

**No — not for the external API.** The S3 wire protocol is defined by AWS as
XML. Every S3-compatible client in existence (boto3, aws-sdk-v3, s3cmd, rclone,
mc, restic, Cyberduck, Transmit, aws-cli, …) speaks XML and will break if you
return JSON. This is not a design choice — it is a compatibility constraint.

## What is XML actually used for?

| Direction | Operation | XML? | Alternative? |
|---|---|---|---|
| Client → Proxy | `DeleteObjects` body | ✅ Required | No |
| Client → Proxy | `CompleteMultipartUpload` body | ✅ Required | No |
| Client → Proxy | `CreateBucket` body (location constraint) | ✅ Required | No |
| Proxy → Client | All error responses | ✅ Required | No |
| Proxy → Client | `ListBuckets` | ✅ Required | No |
| Proxy → Client | `ListObjects` V1/V2 | ✅ Required | No |
| Proxy → Client | `CreateMultipartUpload` | ✅ Required | No |
| Proxy → Client | `ListParts` | ✅ Required | No |
| Proxy → Client | `DeleteObjects` result | ✅ Required | No |
| Proxy ↔ Backend | All backend communication | Handled by `@aws-sdk/client-s3` | SDK internally |
| KV storage | Tenant records | ❌ Not XML | **JSON** ✅ |
| KV storage | Upload state | ❌ Not XML | **JSON** ✅ |
| Admin API | Provisioning script | ❌ Not XML | **JSON** ✅ |

## What about MinIO's JSON API?

MinIO exposes a separate admin API and some endpoints accept JSON, but its
**S3-compatible API** is still XML. Clients that use MinIO as an S3 backend
(rclone, restic, etc.) use the S3-compatible XML endpoints.

## Internal vs External

- **External (S3 API surface)** → XML required for spec compliance
- **Internal (KV, admin, upload state)** → JSON everywhere

## Why `fast-xml-parser` and not something heavier?

The proxy only needs to *parse* two incoming XML bodies:
1. `<Delete>` (DeleteObjects)
2. `<CompleteMultipartUpload>` (Complete MPU)

The `@aws-sdk/client-s3` handles all backend XML internally.
`fast-xml-parser` is 50 KB, zero native deps, zero WASM, and parses in one
pass. An alternative like `xml2js` adds 200 KB and has known prototype
pollution vulnerabilities. A DOM-based parser is not available in Workers.

## XML generation

XML responses are generated with string concatenation (no library) because:
1. S3 XML schemas are static and known at compile time
2. All values are XML-escaped via a single `xmlEscape()` function
3. No library adds correctness — only dependency weight
