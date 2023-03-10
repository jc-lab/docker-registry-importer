package common

type AppFlags struct {
	Url        *string
	Proxy      *string
	File       *string
	Username   *string
	Password   *string
	ConfigFile *string

	IsImport *bool
	IsExport *bool

	IncludeRepoName *bool

	CacheDir *string

	ImageList []string
	Config    *Config
}
