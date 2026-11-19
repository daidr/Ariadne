package settings

type UpdatePreferences struct {
	Channel   string `json:"channel"`
	Automatic bool   `json:"automatic"`
}

func (value UpdatePreferences) Valid() bool {
	return value.Channel == "stable" || value.Channel == "preview"
}

func (store *Store) UpdatePreferences(defaultChannel string) UpdatePreferences {
	stored := object(store.snapshot()["updates"])
	value := UpdatePreferences{Channel: text(stored["channel"]), Automatic: stored["automatic"] == true}
	if !value.Valid() {
		value.Channel = defaultChannel
	}
	return value
}

func (store *Store) SaveUpdatePreferences(value UpdatePreferences) error {
	if !value.Valid() {
		return bad("channel")
	}
	return store.update(func(config Object) error {
		config["updates"] = Object{"channel": value.Channel, "automatic": value.Automatic}
		return nil
	})
}
