package server

// Provider capabilities are explicit: not every metadata site has actor catalogs.
func catalogProviders() []Object {
	return []Object{
		{"source": "javdbui", "name": "JavDB UI", "default_url": "https://jdforrepam.com/api", "supports_actor_catalog": false, "supports_recommendations": true},
		{"source": "javdb", "name": "JavDB", "default_url": "https://javdb.com", "supports_actor_catalog": true},
		{"source": "javlibrary", "name": "JavLibrary", "default_url": "https://www.javlibrary.com", "supports_actor_catalog": true},
		{"source": "fanza", "name": "FANZA", "default_url": "https://www.dmm.co.jp", "supports_actor_catalog": true},
		{"source": "javbus", "name": "JavBus", "default_url": "https://www.javbus.com", "supports_actor_catalog": true},
		{"source": "jav321", "name": "Jav321", "default_url": "https://www.jav321.com", "supports_actor_catalog": false},
	}
}
