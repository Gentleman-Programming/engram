package dashboard

type TemplRuntimePolicy struct {
	Mode                     string
	RuntimeGenerationAllowed bool
	GenerateCommand          string
}

func templRuntimePolicy() TemplRuntimePolicy {
	return TemplRuntimePolicy{
		Mode:                     "checked-in-generated",
		RuntimeGenerationAllowed: false,
		GenerateCommand:          "go tool templ generate ./internal/cloud/dashboard/...",
	}
}
