import { describe, it, expect } from "vitest";
import {
  parseDeleteRequest, parseCompleteMpu,
  xmlListBuckets, xmlListObjectsV2, xmlListObjectsV1,
  xmlCreateMpu, xmlCompleteResult, xmlListParts, xmlDeleteResult, xmlCopyObjectResult,
} from "../src/protocol/xml";

// ─── parseDeleteRequest ───────────────────────────────────────────────────────

describe("parseDeleteRequest", () => {
  it("parses single object", () => {
    const r = parseDeleteRequest(`<Delete><Object><Key>a/b.txt</Key></Object></Delete>`);
    expect(r.objects).toHaveLength(1);
    expect(r.objects[0]!.Key).toBe("a/b.txt");
    expect(r.quiet).toBe(false);
  });

  it("parses multiple objects", () => {
    const r = parseDeleteRequest(`<Delete>
      <Object><Key>x</Key></Object>
      <Object><Key>y</Key></Object>
      <Object><Key>z</Key></Object>
    </Delete>`);
    expect(r.objects).toHaveLength(3);
    expect(r.objects.map((o) => o.Key)).toEqual(["x", "y", "z"]);
  });

  it("parses Quiet=true", () => {
    const r = parseDeleteRequest(`<Delete><Quiet>true</Quiet><Object><Key>k</Key></Object></Delete>`);
    expect(r.quiet).toBe(true);
  });

  it("parses empty Delete body (no objects)", () => {
    const r = parseDeleteRequest(`<Delete></Delete>`);
    expect(r.objects).toHaveLength(0);
  });

  it("parses XML-encoded key values", () => {
    const r = parseDeleteRequest(`<Delete><Object><Key>path/file&amp;name.txt</Key></Object></Delete>`);
    expect(r.objects[0]!.Key).toBe("path/file&name.txt");
  });

  it("throws on missing Delete root", () => {
    expect(() => parseDeleteRequest(`<Other/>`)).toThrow("Missing <Delete>");
  });

  it("throws on empty body", () => {
    expect(() => parseDeleteRequest("")).toThrow();
  });

  it("throws on malformed XML", () => {
    expect(() => parseDeleteRequest("<Delete><unclosed>")).toThrow();
  });

  it("throws when Key is missing from Object", () => {
    expect(() => parseDeleteRequest(`<Delete><Object></Object></Delete>`)).toThrow();
  });
});

// ─── parseCompleteMpu ─────────────────────────────────────────────────────────

describe("parseCompleteMpu", () => {
  const VALID = `<CompleteMultipartUpload>
    <Part><PartNumber>1</PartNumber><ETag>"aa"</ETag></Part>
    <Part><PartNumber>2</PartNumber><ETag>"bb"</ETag></Part>
  </CompleteMultipartUpload>`;

  it("parses valid body", () => {
    const r = parseCompleteMpu(VALID);
    expect(r.parts).toHaveLength(2);
    expect(r.parts[0]).toEqual({ PartNumber: 1, ETag: '"aa"' });
    expect(r.parts[1]).toEqual({ PartNumber: 2, ETag: '"bb"' });
  });

  it("parses empty part list", () => {
    const r = parseCompleteMpu(`<CompleteMultipartUpload></CompleteMultipartUpload>`);
    expect(r.parts).toHaveLength(0);
  });

  it("parses single part (not wrapped in array)", () => {
    const r = parseCompleteMpu(`<CompleteMultipartUpload>
      <Part><PartNumber>5</PartNumber><ETag>"cc"</ETag></Part>
    </CompleteMultipartUpload>`);
    expect(r.parts[0]!.PartNumber).toBe(5);
  });

  it("throws on invalid PartNumber (non-integer)", () => {
    expect(() => parseCompleteMpu(`<CompleteMultipartUpload>
      <Part><PartNumber>abc</PartNumber><ETag>"x"</ETag></Part>
    </CompleteMultipartUpload>`)).toThrow("PartNumber");
  });

  it("throws on PartNumber > 10000", () => {
    expect(() => parseCompleteMpu(`<CompleteMultipartUpload>
      <Part><PartNumber>10001</PartNumber><ETag>"x"</ETag></Part>
    </CompleteMultipartUpload>`)).toThrow("PartNumber");
  });

  it("throws on missing ETag", () => {
    expect(() => parseCompleteMpu(`<CompleteMultipartUpload>
      <Part><PartNumber>1</PartNumber></Part>
    </CompleteMultipartUpload>`)).toThrow("ETag");
  });

  it("throws on missing root element", () => {
    expect(() => parseCompleteMpu(`<Other/>`)).toThrow("Missing");
  });

  it("throws on empty body", () => {
    expect(() => parseCompleteMpu("")).toThrow();
  });
});

// ─── xmlListBuckets ───────────────────────────────────────────────────────────

describe("xmlListBuckets", () => {
  it("contains required fields", () => {
    const xml = xmlListBuckets("alice", "Alice Corp", [
      { name: "photos", createdAt: "2024-01-01T00:00:00.000Z" },
    ]);
    expect(xml).toContain('<ListAllMyBucketsResult');
    expect(xml).toContain("<ID>alice</ID>");
    expect(xml).toContain("<DisplayName>Alice Corp</DisplayName>");
    expect(xml).toContain("<Name>photos</Name>");
    expect(xml).toContain("<CreationDate>2024-01-01T00:00:00.000Z</CreationDate>");
  });

  it("handles empty bucket list", () => {
    const xml = xmlListBuckets("t", "T", []);
    expect(xml).toContain("<Buckets>");
    expect(xml).not.toContain("<Bucket>");
  });

  it("XML-escapes all values", () => {
    const xml = xmlListBuckets("t&<>\"'", "Name&<>\"'", [
      { name: "b&<>", createdAt: "2024-01-01T00:00:00.000Z" },
    ]);
    expect(xml).not.toMatch(/[^>]&[^a-z#][^;]*</); // no unescaped &
    expect(xml).toContain("&amp;");
    expect(xml).toContain("&lt;");
  });
});

// ─── xmlListObjectsV2 ────────────────────────────────────────────────────────

describe("xmlListObjectsV2", () => {
  const BASE = {
    bucket: "my-bucket", prefix: "", delimiter: "", maxKeys: 1000,
    isTruncated: false, keyCount: 0, contents: [], commonPrefixes: [],
  };

  it("contains xmlns declaration", () => {
    const xml = xmlListObjectsV2(BASE);
    expect(xml).toContain('xmlns="http://s3.amazonaws.com/doc/2006-03-01/"');
  });

  it("renders content objects", () => {
    const xml = xmlListObjectsV2({
      ...BASE,
      keyCount: 1,
      contents: [{
        key: "dir/file.txt", lastModified: "2024-01-15T12:00:00Z",
        etag: '"abc"', size: 1024n, storageClass: "STANDARD",
      }],
    });
    expect(xml).toContain("<Key>dir/file.txt</Key>");
    expect(xml).toContain("<Size>1024</Size>");
    expect(xml).toContain("<ETag>&#34;abc&#34;</ETag>"); // or <ETag>&quot;abc&quot;</ETag>
  });

  it("renders common prefixes", () => {
    const xml = xmlListObjectsV2({ ...BASE, commonPrefixes: ["folder/"] });
    expect(xml).toContain("<Prefix>folder/</Prefix>");
  });

  it("includes NextContinuationToken when truncated", () => {
    const xml = xmlListObjectsV2({ ...BASE, isTruncated: true, nextContinuationToken: "tok123" });
    expect(xml).toContain("<IsTruncated>true</IsTruncated>");
    expect(xml).toContain("<NextContinuationToken>tok123</NextContinuationToken>");
  });

  it("omits optional fields when absent", () => {
    const xml = xmlListObjectsV2(BASE);
    expect(xml).not.toContain("<Delimiter>");
    expect(xml).not.toContain("<ContinuationToken>");
    expect(xml).not.toContain("<NextContinuationToken>");
    expect(xml).not.toContain("<StartAfter>");
  });

  it("renders BigInt sizes correctly (no 'n' suffix)", () => {
    const xml = xmlListObjectsV2({
      ...BASE, keyCount: 1,
      contents: [{ key: "f", lastModified: "", etag: '""', size: 5_000_000_000n, storageClass: "STANDARD" }],
    });
    expect(xml).toContain("<Size>5000000000</Size>");
    expect(xml).not.toContain("5000000000n");
  });
});

// ─── xmlDeleteResult ─────────────────────────────────────────────────────────

describe("xmlDeleteResult", () => {
  it("renders deleted and error entries", () => {
    const xml = xmlDeleteResult(
      ["ok.txt"],
      [{ key: "fail.txt", code: "AccessDenied", message: "Permission denied" }],
    );
    expect(xml).toContain("<Deleted><Key>ok.txt</Key></Deleted>");
    expect(xml).toContain("<Code>AccessDenied</Code>");
    expect(xml).toContain("<Message>Permission denied</Message>");
  });

  it("renders all-deleted case", () => {
    const xml = xmlDeleteResult(["a", "b"], []);
    expect(xml).not.toContain("<Error>");
  });

  it("renders all-error case", () => {
    const xml = xmlDeleteResult([], [{ key: "a", code: "InternalError", message: "Oops" }]);
    expect(xml).not.toContain("<Deleted>");
  });
});

// ─── xmlCreateMpu ────────────────────────────────────────────────────────────

describe("xmlCreateMpu", () => {
  it("contains all required fields", () => {
    const xml = xmlCreateMpu("b", "k/p.bin", "upload-uuid");
    expect(xml).toContain("<Bucket>b</Bucket>");
    expect(xml).toContain("<Key>k/p.bin</Key>");
    expect(xml).toContain("<UploadId>upload-uuid</UploadId>");
    expect(xml).toContain("InitiateMultipartUploadResult");
  });
});

// ─── xmlCompleteResult ───────────────────────────────────────────────────────

describe("xmlCompleteResult", () => {
  it("contains ETag and Location", () => {
    const xml = xmlCompleteResult("bucket", "key", '"final-etag"', "https://backend/bucket/key");
    expect(xml).toContain("CompleteMultipartUploadResult");
    expect(xml).toContain("<ETag>");
    expect(xml).toContain("<Location>");
    expect(xml).toContain("<Bucket>bucket</Bucket>");
    expect(xml).toContain("<Key>key</Key>");
  });

  it("falls back to constructed Location when backend returns empty", () => {
    const xml = xmlCompleteResult("b", "k", '"e"', "");
    expect(xml).toContain("<Location>/b/k</Location>");
  });
});
