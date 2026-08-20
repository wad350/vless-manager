package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/mtproxy"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type MTProxyManager struct {
	inbounds map[string]*MTProxyUserManager

	mtx sync.Mutex
}

func NewMTProxyManager() *MTProxyManager {
	return &MTProxyManager{
		inbounds: make(map[string]*MTProxyUserManager),
	}
}

func (m *MTProxyManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &MTProxyUserManager{
		inbound:  inbound.(*mtproxy.Inbound),
		usersMap: make(map[string]option.MTProxyUser),
	}
	return nil
}

func (m *MTProxyManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *MTProxyManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type MTProxyUserManager struct {
	inbound  *mtproxy.Inbound
	usersMap map[string]option.MTProxyUser

	mtx sync.Mutex
}

func (i *MTProxyUserManager) postUpdate() {
	users := make([]option.MTProxyUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *MTProxyUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.MTProxyUser{Name: user.Username, Secret: user.Secret}
	i.postUpdate()
}

func (i *MTProxyUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.MTProxyUser{Name: user.Username, Secret: user.Secret}
	}
	i.postUpdate()
}

func (i *MTProxyUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
