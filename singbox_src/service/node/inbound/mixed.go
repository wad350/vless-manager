package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/protocol/mixed"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
	"github.com/sagernet/sing/common/auth"
)

type MixedManager struct {
	inbounds map[string]*MixedUserManager

	mtx sync.Mutex
}

func NewMixedManager() *MixedManager {
	return &MixedManager{
		inbounds: make(map[string]*MixedUserManager),
	}
}

func (m *MixedManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &MixedUserManager{
		inbound:  inbound.(*mixed.Inbound),
		usersMap: make(map[string]auth.User),
	}
	return nil
}

func (m *MixedManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *MixedManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type MixedUserManager struct {
	inbound  *mixed.Inbound
	usersMap map[string]auth.User

	mtx sync.Mutex
}

func (i *MixedUserManager) postUpdate() {
	users := make([]auth.User, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *MixedUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = auth.User{Username: user.Username, Password: user.Password}
	i.postUpdate()
}

func (i *MixedUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = auth.User{Username: user.Username, Password: user.Password}
	}
	i.postUpdate()
}

func (i *MixedUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
