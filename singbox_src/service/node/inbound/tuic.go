package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/tuic"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type TUICManager struct {
	inbounds map[string]*TUICUserManager

	mtx sync.Mutex
}

func NewTUICManager() *TUICManager {
	return &TUICManager{
		inbounds: make(map[string]*TUICUserManager),
	}
}

func (m *TUICManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &TUICUserManager{
		inbound:  inbound.(*tuic.Inbound),
		usersMap: make(map[string]option.TUICUser),
	}
	return nil
}

func (m *TUICManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *TUICManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type TUICUserManager struct {
	inbound  *tuic.Inbound
	usersMap map[string]option.TUICUser

	mtx sync.Mutex
}

func (i *TUICUserManager) postUpdate() {
	users := make([]option.TUICUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *TUICUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.TUICUser{Name: user.Username, UUID: user.UUID, Password: user.Password}
	i.postUpdate()
}

func (i *TUICUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.TUICUser{Name: user.Username, UUID: user.UUID, Password: user.Password}
	}
	i.postUpdate()
}

func (i *TUICUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
