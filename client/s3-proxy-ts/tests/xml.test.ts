/**
 * XML layer tests.
 * Parsing uses fast-xml-parser; generation is hand-rolled.
 * Both are tested here independently.
 */
import { describe, it, expect } from "vitest";
import {
  parseDeleteRequest,
  parseCompleteMpu,
  xmlListBuckets,
  xmlListObjectsV2,
  xmlListObjectsV1,
  xmlCreateMpu,
  xmlListParts,
  xmlDeleteResult,
} from "../src/protocol/xml";

// ─── parseDeleteRequest ───────────────────────────────────────────────────────

describe("parseDeleteRequest", () => {
  it("parses a single-object delete request", () => {
    const xml = `<?xml version="1.0"?>
<Delete>
  <Object><Key>photos/sunset.jpg</Key></Object>
</Delete>`;
    const req = parseDeleteRequest(xml);
    expect(req.Objects).toHaveLength(1);
    expect(req.Objects[0]!.Key).toBe("photos/sunset.jpg");
    expect(req.Quiet).toBe(false);
  });

  it("parses multiple objects", () => {
    const xml = `<Delete>
  <Object><Key>a.txt</Key></Object>
  <Object><Key>b.txt</Key></Object>
  <Object><Key>deep/nested/c.bin</Key></Object>
</Delete>`;
    const req = parseDeleteRequest(xml);
    expect(req.Objects).toHaveLength(3);
    expect(req.Objects[2]!.Key).toBe("deep/nested/c.bin");
  });

  it("parses Quiet=true flag", () => {
    const xml = `<Delete><Quiet>true</Quiet><Object><Key>x</Key></Object></Delete>`;
    const req = parseDeleteRequest(xml);
    expect(req.Quiet).toBe(true);
  });

  it("returns empty Objects array for empty Delete element", () => {
    const xml = `<Delete></Delete>`;
    const req = parseDeleteRequest(xml);
    expect(req.Objects).toHaveLength(0);
  });

  it("throws on missing root element", () => {
    expect(() => parseDeleteRequest("<NotDelete/>")).toThrow(
      /Missing <Delete>/
    );
  });

  it("throws on malformed XML", () => {
    expect(() => parseDeleteRequest("<Delete><unclosed>")).toThrow();
  });

  it("handles keys with special characters", () => {
    const xml = `<Delete>
  <Object><Key>path/to/file with spaces.txt</Key></Object>
  <Object><Key>path/to/file&amp;name.txt</Key></Object>
</Delete>`;
    const req = parseDeleteRequest(xml);
    expect(req.Objects[0]!.Key).toBe("path/to/file with spaces.txt");
    expect(req.Objects[1]!.Key).toBe("path/to/file&name.txt");
  });
});

// ─── parseCompleteMpu ─────────────────────────────────────────────────────────

describe("parseCompleteMpu", () => {
  it("parses a valid CompleteMultipartUpload body", () => {
    const xml = `<CompleteMultipartUpload>
  <Part><PartNumber>1</PartNumber><ETag>"etag-part-1"</ETag></Part>
  <Part><PartNumber>2</PartNumber><ETag>"etag-part-2"</ETag></Part>
  <Part><PartNumber>3</PartNumber><ETag>"etag-part-3"</ETag></Part>
</CompleteMultipartUpload>`;
    const req = parseCompleteMpu(xml);
    expect(req.Parts).toHaveLength(3);
    expect(req.Parts[0]!.PartNumber).toBe(1);
    expect(req.Parts[0]!.ETag).toBe('"etag-part-1"');
    expect(req.Parts[2]!.PartNumber).toBe(3);
  });

  it("throws on missing root element", () => {
    expect(() => parseCompleteMpu("<Other/>")).toThrow(
      /Missing <CompleteMultipartUpload>/
    );
  });

  it("throws on invalid PartNumber", () => {
    const xml = `<CompleteMultipartUpload>
  <Part><PartNumber>not-a-number</PartNumber><ETag>"x"</ETag></Part>
</CompleteMultipartUpload>`;
    expect(() => parseCompleteMpu(xml)).toThrow(/PartNumber/);
  });

  it("throws on PartNumber out of range", () => {
    const xml = `<CompleteMultipartUpload>
  <Part><PartNumber>10001</PartNumber><ETag>"x"</ETag></Part>
</CompleteMultipartUpload>`;
    expect(() => parseCompleteMpu(xml)).toThrow(/PartNumber/);
  });

  it("handles empty part list", () => {
    const xml = `<CompleteMultipartUpload></CompleteMultipartUpload>`;
    const req = parseCompleteMpu(xml);
    expect(req.Parts).toHaveLength(0);
  });
});

// ─── xmlListBuckets ───────────────────────────────────────────────────────────

describe("xmlListBuckets", () => {
  it("generates correct XML structure", () => {
    const xml = xmlListBuckets("alice", [
      { name: "photos", createdAt: "2024-01-01T00:00:00Z" },
      { name: "backups", createdAt: "2024-02-01T00:00:00Z" },
    ]);
    expect(xml).toContain("<Name>photos</Name>");
    expect(xml).toContain("<Name>backups</Name>");
    expect(xml).toContain("<ID>alice</ID>");
    expect(xml).toContain("ListAllMyBucketsResult");
    expect(xml).toContain("http://s3.amazonaws.com/doc/2006-03-01/");
  });

  it("handles empty bucket list", () => {
    const xml = xmlListBuckets("tenant-1", []);
    expect(xml).toContain("<Buckets>");
    expect(xml).not.toContain("<Bucket>");
  });

  it("XML-escapes bucket names with special chars", () => {
    const xml = xmlListBuckets("t", [
      { name: "a&b<c>d", createdAt: "2024-01-01T00:00:00Z" },
    ]);
    expect(xml).toContain("a&amp;b&lt;c&gt;d");
    expect(xml).not.toMatch(/<Name>[^<]*&[^a][^<]*<\/Name>/);
  });
});

// ─── xmlListObjectsV2 ────────────────────────────────────────────────────────

describe("xmlListObjectsV2", () => {
  const baseParams = {
    bucket: "my-bucket",
    prefix: "photos/",
    delimiter: "/",
    maxKeys: 1000,
    isTruncated: false,
    keyCount: 2,
    contents: [
      {
        key: "photos/a.jpg",
        lastModified: "2024-01-15T12:00:00Z",
        etag: '"etag-a"',
        size: 1024,
        storageClass: "STANDARD",
      },
      {
        key: "photos/b.jpg",
        lastModified: "2024-01-16T12:00:00Z",
        etag: '"etag-b"',
        size: 2048,
        storageClass: "STANDARD",
      },
    ],
    commonPrefixes: ["photos/2024/"],
  };

  it("contains all required fields", () => {
    const xml = xmlListObjectsV2(baseParams);
    expect(xml).toContain("<Name>my-bucket</Name>");
    expect(xml).toContain("<Prefix>photos/</Prefix>");
    expect(xml).toContain("<MaxKeys>1000</MaxKeys>");
    expect(xml).toContain("<KeyCount>2</KeyCount>");
    expect(xml).toContain("<IsTruncated>false</IsTruncated>");
    expect(xml).toContain("<Key>photos/a.jpg</Key>");
    expect(xml).toContain("<Size>1024</Size>");
    expect(xml).toContain(
      "<CommonPrefixes><Prefix>photos/2024/</Prefix></CommonPrefixes>"
    );
  });

  it("includes NextContinuationToken when truncated", () => {
    const xml = xmlListObjectsV2({
      ...baseParams,
      isTruncated: true,
      nextContinuationToken: "next-page-token-xyz",
    });
    expect(xml).toContain("<IsTruncated>true</IsTruncated>");
    expect(xml).toContain(
      "<NextContinuationToken>next-page-token-xyz</NextContinuationToken>"
    );
  });

  it("XML-escapes keys with special characters", () => {
    const xml = xmlListObjectsV2({
      ...baseParams,
      contents: [
        {
          key: "path/<weird>&key.txt",
          lastModified: "",
          etag: '""',
          size: 0,
          storageClass: "STANDARD",
        },
      ],
      keyCount: 1,
    });
    expect(xml).toContain("&lt;weird&gt;");
    expect(xml).toContain("&amp;key");
    expect(xml).not.toContain("<Key>path/<");
  });

  it("omits optional fields when not provided", () => {
    const xml = xmlListObjectsV2({
      ...baseParams,
      delimiter: "",
      continuationToken: undefined,
    });
    expect(xml).not.toContain("<Delimiter>");
    expect(xml).not.toContain("<ContinuationToken>");
  });
});

// ─── xmlListObjectsV1 ────────────────────────────────────────────────────────

describe("xmlListObjectsV1", () => {
  it("contains Marker and optional NextMarker when truncated", () => {
    const xml = xmlListObjectsV1({
      bucket: "b",
      prefix: "",
      delimiter: "",
      marker: "last-seen-key.txt",
      maxKeys: 100,
      isTruncated: true,
      nextMarker: "last-seen-key.txt",
      contents: [
        {
          key: "last-seen-key.txt",
          lastModified: "",
          etag: '""',
          size: 0,
          storageClass: "STANDARD",
        },
      ],
      commonPrefixes: [],
    });
    expect(xml).toContain("<Marker>last-seen-key.txt</Marker>");
    expect(xml).toContain("<NextMarker>last-seen-key.txt</NextMarker>");
    expect(xml).toContain("<IsTruncated>true</IsTruncated>");
  });
});

// ─── xmlCreateMpu ─────────────────────────────────────────────────────────────

describe("xmlCreateMpu", () => {
  it("contains all three required fields", () => {
    const xml = xmlCreateMpu(
      "photos",
      "2024/big-file.bin",
      "proxy-upload-uuid"
    );
    expect(xml).toContain("<Bucket>photos</Bucket>");
    expect(xml).toContain("<Key>2024/big-file.bin</Key>");
    expect(xml).toContain("<UploadId>proxy-upload-uuid</UploadId>");
    expect(xml).toContain("InitiateMultipartUploadResult");
  });
});

// ─── xmlListParts ─────────────────────────────────────────────────────────────

describe("xmlListParts", () => {
  it("lists all parts with correct fields", () => {
    const xml = xmlListParts("b", "k", "uid", [
      {
        partNumber: 1,
        etag: '"etag-1"',
        size: 5_242_880,
        lastModified: "2024-01-15T12:00:00Z",
      },
      {
        partNumber: 2,
        etag: '"etag-2"',
        size: 5_242_880,
        lastModified: "2024-01-15T12:05:00Z",
      },
    ]);
    expect(xml).toContain("<PartNumber>1</PartNumber>");
    expect(xml).toContain("<PartNumber>2</PartNumber>");
    expect(xml).toContain("<Size>5242880</Size>");
    expect(xml).toContain("ListPartsResult");
  });
});

// ─── xmlDeleteResult ─────────────────────────────────────────────────────────

describe("xmlDeleteResult", () => {
  it("separates deleted keys from errors", () => {
    const xml = xmlDeleteResult(
      ["a.txt", "b.txt"],
      [{ key: "c.txt", message: "Access Denied" }]
    );
    expect(xml).toContain("<Deleted><Key>a.txt</Key></Deleted>");
    expect(xml).toContain("<Deleted><Key>b.txt</Key></Deleted>");
    expect(xml).toContain("<Error>");
    expect(xml).toContain("<Key>c.txt</Key>");
    expect(xml).toContain("<Message>Access Denied</Message>");
  });

  it("produces valid XML when all deleted", () => {
    const xml = xmlDeleteResult(["x.bin"], []);
    expect(xml).toContain("<Deleted>");
    expect(xml).not.toContain("<Error>");
  });

  it("produces valid XML when all errors", () => {
    const xml = xmlDeleteResult([], [{ key: "y.bin", message: "No such key" }]);
    expect(xml).not.toContain("<Deleted>");
    expect(xml).toContain("<Error>");
  });

  it("XML-escapes error messages with special chars", () => {
    const xml = xmlDeleteResult(
      [],
      [{ key: "z.txt", message: 'Error: "access" < denied' }]
    );
    expect(xml).toContain("&quot;access&quot;");
    expect(xml).toContain("&lt; denied");
  });
});
