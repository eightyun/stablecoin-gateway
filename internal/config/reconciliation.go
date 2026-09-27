package config

// ReconciliationConfig 保存一次性对账任务配置。
type ReconciliationConfig struct {
	DatabaseURL string
}

// LoadReconciliation 加载一次性对账任务配置。
func LoadReconciliation() (ReconciliationConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return ReconciliationConfig{}, err
	}
	return ReconciliationConfig{DatabaseURL: databaseURL}, nil
}
