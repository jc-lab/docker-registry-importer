package importer

import (
	"archive/tar"
	"encoding/json"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/manifest/schema1"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/jc-lab/docker-registry-importer/common"
	"github.com/jc-lab/docker-registry-importer/internal/registry"
	"github.com/opencontainers/go-digest"
	"io"
	"log"
	"os"
	"regexp"
)

type ManifestFile struct {
	repository  string
	name        string
	digestType  string
	digestValue string
	tag         string
	data        []byte

	manifestV1 *schema1.Manifest
	manifestV2 *schema2.Manifest
}

type BlobItem struct {
	uploaded  bool
	manifests []*ManifestFile
	size      int64
}

type ImportContext struct {
	Registry  *registry.Registry
	manifests []*ManifestFile
	blobs     map[string]*BlobItem
}

var regexpManifestFile, _ = regexp.Compile("^(.+)/manifests/([^/:]+):(.+)$")
var regexpTagManifestFile, _ = regexp.Compile("^(.+)/manifests/([^/:]+)$")
var regexpTagFile, _ = regexp.Compile("^(.+)/tags/(.+)$")
var regxpBlobFile, _ = regexp.Compile("^blob/([^/:]+):(.+)$")

func (ctx *ImportContext) DoImport(flags *common.AppFlags) {
	err := ctx.parseArchive(*flags.File)
	if err != nil {
		log.Fatalln(err)
	}

	err = ctx.uploadBlobs(*flags.File)
	if err != nil {
		log.Fatalln(err)
	}

	err = ctx.uploadManifests()
	if err != nil {
		log.Fatalln(err)
	}
}

func (ctx *ImportContext) parseArchive(file string) error {
	ctx.manifests = make([]*ManifestFile, 0)
	ctx.blobs = make(map[string]*BlobItem)

	reader, err := os.OpenFile(file, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer reader.Close()

	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF || header == nil {
			break
		} else if err != nil {
			return err
		}

		groups := regexpTagManifestFile.FindStringSubmatch(header.Name)
		if groups != nil {
			repo := groups[1]
			tag := groups[2]

			log.Printf("MANIFEST: " + repo + ":" + tag)

			data, err := io.ReadAll(tarReader)
			if err != nil {
				return err
			}

			item := &ManifestFile{
				repository: repo,
				name:       tag,
				tag:        tag,
				data:       data,
			}
			err = ctx.readManifest(item)
			if err != nil {
				return err
			}
			ctx.manifests = append(ctx.manifests, item)
		}

		groups = regexpManifestFile.FindStringSubmatch(header.Name)
		if groups != nil {
			repo := groups[1]
			digestType := groups[2]
			digestValue := groups[3]

			log.Printf("MANIFEST: " + repo + "@" + digestType + ":" + digestValue)

			data, err := io.ReadAll(tarReader)
			if err != nil {
				return err
			}

			item := &ManifestFile{
				repository:  repo,
				name:        digestType + ":" + digestValue,
				digestType:  digestType,
				digestValue: digestValue,
				data:        data,
			}
			err = ctx.readManifest(item)
			if err != nil {
				return err
			}
			ctx.manifests = append(ctx.manifests, item)
		}

		//groups = regexpTagFile.FindStringSubmatch(header.Name)
		//if groups != nil {
		//	name := groups[1]
		//	tag := groups[2]
		//
		//	log.Printf("MANIFEST: " + name + ":" + tag)
		//
		//	data, err := io.ReadAll(tarReader)
		//	if err != nil {
		//		return err
		//	}
		//
		//	item := &ManifestFile{
		//		repository: name,
		//		tag:        tag,
		//		data:       data,
		//	}
		//
		//	err = ctx.readManifest(item)
		//	if err != nil {
		//		return err
		//	}
		//	ctx.manifests = append(ctx.manifests, item)
		//}

		groups = regxpBlobFile.FindStringSubmatch(header.Name)
		if groups != nil {
			digestType := groups[1]
			digestValue := groups[2]
			digestFull := digestType + ":" + digestValue
			blob := ctx.blobs[digestFull]
			if blob == nil {
				blob = &BlobItem{
					manifests: make([]*ManifestFile, 0),
				}
				ctx.blobs[digestFull] = blob
			}
			blob.size, err = common.IoConsumeAll(tarReader)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (ctx *ImportContext) readManifest(item *ManifestFile) error {
	var manifestVersion manifest.Versioned
	err := json.Unmarshal(item.data, &manifestVersion)
	if err != nil {
		return err
	}

	switch manifestVersion.SchemaVersion {
	case 1:
		item.manifestV1 = &schema1.Manifest{}
		err = json.Unmarshal(item.data, item.manifestV1)
		if err != nil {
			return err
		}
		for _, v := range item.manifestV1.FSLayers {
			blob := ctx.blobs[v.BlobSum.String()]
			if blob == nil {
				blob = &BlobItem{
					manifests: make([]*ManifestFile, 0),
				}
				ctx.blobs[v.BlobSum.String()] = blob
			}
			blob.manifests = append(blob.manifests, item)
		}

		//packedManifest = fromSchemaV1(item.data, item.manifestV1)
		break
	case 2:
		item.manifestV2 = &schema2.Manifest{}
		err = json.Unmarshal(item.data, item.manifestV2)
		if err != nil {
			return err
		}

		for _, v := range append(item.manifestV2.Layers, item.manifestV2.Config) {
			blob := ctx.blobs[v.Digest.String()]
			if blob == nil {
				blob = &BlobItem{
					manifests: make([]*ManifestFile, 0),
				}
				ctx.blobs[v.Digest.String()] = blob
			}
			blob.manifests = append(blob.manifests, item)
		}
		break
	}

	return nil
}

func (ctx *ImportContext) uploadBlobs(file string) error {
	reader, err := os.OpenFile(file, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer reader.Close()

	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF || header == nil {
			break
		} else if err != nil {
			return err
		}

		groups := regxpBlobFile.FindStringSubmatch(header.Name)
		if groups != nil {
			digestType := groups[1]
			digestValue := groups[2]
			digestFull := digestType + ":" + digestValue
			blob := ctx.blobs[digestFull]
			if blob == nil {
				log.Printf("empty blob: " + digestValue)
				continue
			}
			manifest := blob.manifests[0]

			log.Printf("UPLOAD BLOB: " + digestValue + " (" + manifest.repository + ") START")

			d := digest.NewDigestFromHex(digestType, digestValue)

			has, _ := ctx.Registry.HasBlob(manifest.repository, d)
			if has {
				log.Printf("UPLOAD BLOB: " + d.String() + " (" + manifest.repository + ") ALREADY EXISTS")
			} else {
				err := ctx.Registry.UploadBlob(manifest.repository, d, tarReader, blob.size)
				if err == nil {
					log.Printf("UPLOAD BLOB: " + d.String() + " (" + manifest.repository + ") SUCCESS")
				} else {
					log.Printf("UPLOAD BLOB: " + d.String() + " (" + manifest.repository + ") FAILED: " + err.Error())
				}
			}
		}
	}

	return nil
}

func (ctx *ImportContext) uploadManifests() error {
	for _, item := range ctx.manifests {
		if item.manifestV2 != nil {
			fullName := item.repository
			if len(item.tag) > 0 {
				fullName += ":" + item.name
			} else {
				fullName += "@" + item.name
			}

			m := &schema2.DeserializedManifest{}
			err := m.UnmarshalJSON(item.data)
			if err != nil {
				log.Printf("Put Manifest "+fullName+" FAILED: ", err)
				continue
			}

			err = ctx.Registry.PutManifest(item.repository, item.name, m)
			if err != nil {
				log.Printf("Put Manifest "+fullName+" FAILED: ", err)
				continue
			}

			log.Printf("Put Manifest " + fullName + " SUCCESS")
		}
	}
	return nil
}
