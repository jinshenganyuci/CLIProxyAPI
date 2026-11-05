package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	log "github.com/sirupsen/logrus"
)

func (s *Service) applyKVStoreConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	s.kvStoreMu.Lock()
	defer s.kvStoreMu.Unlock()
	if s.kvStoreClosed {
		return
	}
	s.kvStoreManaged = true
	s.configureKVStoreLocked(cfg)
}

func (s *Service) reloadKVStoreConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	s.kvStoreMu.Lock()
	defer s.kvStoreMu.Unlock()
	if !s.kvStoreManaged || s.kvStoreClosed {
		return
	}
	s.configureKVStoreLocked(cfg)
}

func (s *Service) configureKVStoreLocked(cfg *config.Config) {
	if cfg == nil || cfg.Home.Enabled || home.Current() != nil || cfg.KVStore.Disabled {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			log.Errorf("failed to shutdown local kv store: %v", errShutdown)
		}
		return
	}
	_ = kvstore.ConfigureLocal(true, cfg.EffectiveKVStorePath())
}

func (s *Service) shutdownKVStore() error {
	if s == nil {
		return nil
	}
	s.kvStoreMu.Lock()
	defer s.kvStoreMu.Unlock()
	s.kvStoreClosed = true
	if !s.kvStoreManaged {
		return nil
	}
	s.kvStoreManaged = false
	return kvstore.ShutdownLocal()
}
