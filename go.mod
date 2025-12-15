module forgejo.org

go 1.25.0

toolchain go1.25.5

require (
	code.forgejo.org/f3/gof3/v3 v3.11.1
	code.forgejo.org/forgejo-contrib/go-libravatar v0.0.0-20191008002943-06d1c002b251
	code.forgejo.org/forgejo/actions-proto v0.5.3
	code.forgejo.org/forgejo/go-rpmutils v1.0.0
	code.forgejo.org/forgejo/levelqueue v1.0.0
	code.forgejo.org/forgejo/reply v1.0.2
	code.forgejo.org/forgejo/runner/v12 v12.2.0
	code.forgejo.org/go-chi/binding v1.0.1
	code.forgejo.org/go-chi/cache v1.0.1
	code.forgejo.org/go-chi/captcha v1.0.2
	code.forgejo.org/go-chi/session v1.0.2
	code.gitea.io/sdk/gitea v0.21.0
	code.superseriousbusiness.org/exif-terminator v0.11.0
	code.superseriousbusiness.org/go-jpeg-image-structure/v2 v2.3.0
	codeberg.org/gusted/mcaptcha v0.0.0-20220723083913-4f3072e1d570
	connectrpc.com/connect v1.19.1
	github.com/42wim/httpsig v1.2.3
	github.com/42wim/sshsig v0.0.0-20250502153856-5100632e8920
	github.com/Azure/go-ntlmssp v0.0.0-20221128193559-754e69321358
	github.com/ProtonMail/go-crypto v1.3.0
	github.com/PuerkitoBio/goquery v1.10.3
	github.com/SaveTheRbtz/zstd-seekable-format-go/pkg v0.7.2
	github.com/alecthomas/chroma/v2 v2.20.0
	github.com/blakesmith/ar v0.0.0-20190502131153-809d4375e1fb
	github.com/blevesearch/bleve/v2 v2.5.6
	github.com/buildkite/terminal-to-html/v3 v3.16.8
	github.com/caddyserver/certmagic v0.24.0
	github.com/chi-middleware/proxy v1.1.1
	github.com/djherbis/buffer v1.2.0
	github.com/djherbis/nio/v3 v3.0.1
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707
	github.com/dsoprea/go-exif/v3 v3.0.1
	github.com/dustin/go-humanize v1.0.1
	github.com/editorconfig/editorconfig-core-go/v2 v2.6.3
	github.com/emersion/go-imap v1.2.1
	github.com/felixge/fgprof v0.9.5
	github.com/fsnotify/fsnotify v1.9.0
	github.com/gliderlabs/ssh v0.3.8
	github.com/go-ap/activitypub v0.0.0-20231114162308-e219254dc5c9
	github.com/go-ap/jsonld v0.0.0-20250905102310-8480b0fe24d9
	github.com/go-chi/chi/v5 v5.2.3
	github.com/go-chi/cors v1.2.2
	github.com/go-co-op/gocron v1.37.0
	github.com/go-enry/go-enry/v2 v2.9.2
	github.com/go-ldap/ldap/v3 v3.4.12
	github.com/go-openapi/spec v0.22.1
	github.com/go-sql-driver/mysql v1.9.3
	github.com/go-webauthn/webauthn v0.14.0
	github.com/gobwas/glob v0.2.3
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f
	github.com/gogs/go-gogs-client v0.0.0-20210131175652-1d7215cd8d85
	github.com/golang-jwt/jwt/v5 v5.3.0
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0
	github.com/google/go-github/v74 v74.0.0
	github.com/google/pprof v0.0.0-20251114195745-4902fdda35c8
	github.com/google/uuid v1.6.0
	github.com/gorilla/feeds v1.2.0
	github.com/gorilla/sessions v1.4.0
	github.com/hashicorp/go-version v1.7.0
	github.com/hashicorp/golang-lru/v2 v2.0.7
	github.com/huandu/xstrings v1.5.0
	github.com/inbucket/html2text v0.9.0
	github.com/jackc/pgx/v5 v5.7.6
	github.com/jhillyerd/enmime/v2 v2.2.0
	github.com/json-iterator/go v1.1.12
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51
	github.com/klauspost/compress v1.18.2
	github.com/klauspost/cpuid/v2 v2.2.11
	github.com/markbates/goth v1.80.0
	github.com/mattn/go-isatty v0.0.20
	github.com/mattn/go-sqlite3 v1.14.32
	github.com/meilisearch/meilisearch-go v0.34.0
	github.com/mholt/archives v0.1.5
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/minio/minio-go/v7 v7.0.95
	github.com/msteinert/pam/v2 v2.1.0
	github.com/niklasfasching/go-org v1.9.1
	github.com/olivere/elastic/v7 v7.0.32
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/pquerna/otp v1.4.0
	github.com/prometheus/client_golang v1.21.1
	github.com/redis/go-redis/v9 v9.8.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/sergi/go-diff v1.4.0
	github.com/stretchr/testify v1.11.1
	github.com/syndtr/goleveldb v1.0.0
	github.com/ulikunitz/xz v0.5.15
	github.com/urfave/cli/v3 v3.5.0
	github.com/valyala/fastjson v1.6.4
	github.com/yohcop/openid-go v1.0.1
	github.com/yuin/goldmark v1.7.13
	github.com/yuin/goldmark-highlighting/v2 v2.0.0-20230729083705-37449abec8cc
	gitlab.com/gitlab-org/api/client-go v0.143.2
	go.uber.org/mock v0.6.0
	go.yaml.in/yaml/v3 v3.0.4
	golang.org/x/crypto v0.46.0
	golang.org/x/image v0.33.0
	golang.org/x/net v0.47.0
	golang.org/x/oauth2 v0.34.0
	golang.org/x/sync v0.19.0
	golang.org/x/sys v0.39.0
	golang.org/x/text v0.32.0
	google.golang.org/protobuf v1.36.10
	gopkg.in/gomail.v2 v2.0.0-20160411212932-81ebce5c23df
	gopkg.in/ini.v1 v1.67.0
	mvdan.cc/xurls/v2 v2.5.0
	xorm.io/builder v0.3.13
	xorm.io/xorm v1.3.9
)

require (
	cloud.google.com/go/compute/metadata v0.6.0 // indirect
	code.superseriousbusiness.org/go-png-image-structure/v2 v2.3.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	filippo.io/edwards25519 v1.1.0 // indirect
	git.sr.ht/~mariusor/go-xsd-duration v0.0.0-20220703122237-02e73435a078 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/RoaringBitmap/roaring/v2 v2.4.5 // indirect
	github.com/STARRY-S/zip v0.2.3 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bits-and-blooms/bitset v1.22.0 // indirect
	github.com/blevesearch/bleve_index_api v1.2.11 // indirect
	github.com/blevesearch/geo v0.2.4 // indirect
	github.com/blevesearch/go-faiss v1.0.26 // indirect
	github.com/blevesearch/go-porterstemmer v1.0.3 // indirect
	github.com/blevesearch/gtreap v0.1.1 // indirect
	github.com/blevesearch/mmap-go v1.0.4 // indirect
	github.com/blevesearch/scorch_segment_api/v2 v2.3.13 // indirect
	github.com/blevesearch/segment v0.9.1 // indirect
	github.com/blevesearch/snowballstem v0.9.0 // indirect
	github.com/blevesearch/upsidedown_store_api v1.0.2 // indirect
	github.com/blevesearch/vellum v1.1.0 // indirect
	github.com/blevesearch/zapx/v11 v11.4.2 // indirect
	github.com/blevesearch/zapx/v12 v12.4.2 // indirect
	github.com/blevesearch/zapx/v13 v13.4.2 // indirect
	github.com/blevesearch/zapx/v14 v14.4.2 // indirect
	github.com/blevesearch/zapx/v15 v15.4.2 // indirect
	github.com/blevesearch/zapx/v16 v16.2.7 // indirect
	github.com/bmatcuk/doublestar/v4 v4.9.1 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/sevenzip v1.6.1 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/boombuler/barcode v1.0.1 // indirect
	github.com/bradfitz/gomemcache v0.0.0-20250403215159-8d39553ac7cf // indirect
	github.com/caddyserver/zerossl v0.1.3 // indirect
	github.com/cention-sany/utf7 v0.0.0-20170124080048-26cad61bd60a // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.1 // indirect
	github.com/cyphar/filepath-securejoin v0.6.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/davidmz/go-pageant v1.0.2 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/dsoprea/go-iptc v0.0.0-20200609062250-162ae6b44feb // indirect
	github.com/dsoprea/go-logging v0.0.0-20200710184922-b02d349568dd // indirect
	github.com/dsoprea/go-photoshop-info-format v0.0.0-20200609050348-3db9b63b202c // indirect
	github.com/dsoprea/go-utility/v2 v2.0.0-20221003172846-a3e1774ef349 // indirect
	github.com/emersion/go-sasl v0.0.0-20231106173351-e73c9f7bad43 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-ap/errors v0.0.0-20231003111023-183eef4b31b7 // indirect
	github.com/go-asn1-ber/asn1-ber v1.5.8-0.20250403174932-29230038a667 // indirect
	github.com/go-enry/go-oniguruma v1.2.1 // indirect
	github.com/go-errors/errors v1.4.2 // indirect
	github.com/go-fed/httpsig v1.1.0 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.6.2 // indirect
	github.com/go-git/go-git/v5 v5.16.3 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/go-openapi/jsonpointer v0.22.3 // indirect
	github.com/go-openapi/jsonreference v0.21.3 // indirect
	github.com/go-openapi/swag/conv v0.25.3 // indirect
	github.com/go-openapi/swag/jsonname v0.25.3 // indirect
	github.com/go-openapi/swag/jsonutils v0.25.3 // indirect
	github.com/go-openapi/swag/loading v0.25.3 // indirect
	github.com/go-openapi/swag/stringutils v0.25.3 // indirect
	github.com/go-openapi/swag/typeutils v0.25.3 // indirect
	github.com/go-openapi/swag/yamlutils v0.25.3 // indirect
	github.com/go-webauthn/x v0.1.25 // indirect
	github.com/go-xmlfmt/xmlfmt v0.0.0-20191208150333-d5b6f63a941b // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/golang/geo v0.0.0-20210211234256-740aa86cb551 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/go-tpm v0.9.5 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/gorilla/securecookie v1.1.2 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/kevinburke/ssh_config v1.2.0 // indirect
	github.com/klauspost/pgzip v1.2.6 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/libdns/libdns v1.0.0 // indirect
	github.com/mailru/easyjson v0.9.0 // indirect
	github.com/markbates/going v1.0.3 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-runewidth v0.0.17 // indirect
	github.com/mattn/go-shellwords v1.0.12 // indirect
	github.com/mholt/acmez/v3 v3.1.2 // indirect
	github.com/miekg/dns v1.1.63 // indirect
	github.com/mikelolasagasti/xz v1.0.1 // indirect
	github.com/minio/crc64nvme v1.0.2 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/minio/minlz v1.0.1 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mrjones/oauth v0.0.0-20190623134757-126b35219450 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nwaples/rardecode/v2 v2.2.0 // indirect
	github.com/olekukonko/errors v1.1.0 // indirect
	github.com/olekukonko/ll v0.0.9 // indirect
	github.com/olekukonko/tablewriter v1.0.7 // indirect
	github.com/onsi/ginkgo v1.16.5 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.22 // indirect
	github.com/pjbgf/sha1cd v0.3.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.62.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/rhysd/actionlint v1.7.8 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/skeema/knownhosts v1.3.1 // indirect
	github.com/sorairolake/lzip-go v0.3.8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/ssor/bom v0.0.0-20170718123548-6386211fdfcf // indirect
	github.com/tinylib/msgp v1.3.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	github.com/zeebo/assert v1.3.0 // indirect
	github.com/zeebo/blake3 v0.2.4 // indirect
	go.etcd.io/bbolt v1.4.3 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	go4.org v0.0.0-20230225012048-214862532bf5 // indirect
	golang.org/x/mod v0.30.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/tools v0.39.0 // indirect
	gopkg.in/alexcesaro/quotedprintable.v3 v3.0.0-20150716171945-2caba252f4dc // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/hashicorp/go-version => github.com/6543/go-version v1.3.1

replace github.com/mholt/archiver/v3 => code.forgejo.org/forgejo/archiver/v3 v3.5.1

replace github.com/gliderlabs/ssh => code.forgejo.org/forgejo/ssh v0.0.0-20241211213324-5fc306ca0616

replace git.sr.ht/~mariusor/go-xsd-duration => code.forgejo.org/forgejo/go-xsd-duration v0.0.0-20220703122237-02e73435a078

replace xorm.io/xorm v1.3.9 => code.forgejo.org/xorm/xorm v1.3.9-forgejo.4
