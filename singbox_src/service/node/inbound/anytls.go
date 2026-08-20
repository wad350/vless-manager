package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/anytls"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type AnyTLSManager struct {
	inbounds map[string]*AnyTLSUserManager

	mtx sync.Mutex
}

func NewAnyTLSManager() *AnyTLSManager {
	return &AnyTLSManager{
		inbounds: make(map[string]*AnyTLSUserManager),
	}
}

func (m *AnyTLSManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &AnyTLSUserManager{
		inbound:  inbound.(*anytls.Inbound),
		usersMap: make(map[string]option.AnyTLSUser),
	}
	return nil
}

func (m *AnyTLSManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *AnyTLSManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type AnyTLSUserManager struct {
	inbound  *anytls.Inbound
	usersMap map[string]option.AnyTLSUser

	mtx sync.Mutex
}

func (i *AnyTLSUserManager) postUpdate() {
	users := make([]option.AnyTLSUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *AnyTLSUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.AnyTLSUser{Name: user.Username, Password: user.Password}
	i.postUpdate()
}

func (i *AnyTLSUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.AnyTLSUser{Name: user.Username, Password: user.Password}
	}
	i.postUpdate()
}

func (i *AnyTLSUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
