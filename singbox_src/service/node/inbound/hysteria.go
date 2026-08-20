package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/hysteria"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type HysteriaManager struct {
	inbounds map[string]*HysteriaUserManager

	mtx sync.Mutex
}

func NewHysteriaManager() *HysteriaManager {
	return &HysteriaManager{
		inbounds: make(map[string]*HysteriaUserManager),
	}
}

func (m *HysteriaManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &HysteriaUserManager{
		inbound:  inbound.(*hysteria.Inbound),
		usersMap: make(map[string]option.HysteriaUser),
	}
	return nil
}

func (m *HysteriaManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *HysteriaManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type HysteriaUserManager struct {
	inbound  *hysteria.Inbound
	usersMap map[string]option.HysteriaUser

	mtx sync.Mutex
}

func (i *HysteriaUserManager) postUpdate() {
	users := make([]option.HysteriaUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *HysteriaUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.HysteriaUser{Name: user.Username, AuthString: user.Password}
	i.postUpdate()
}

func (i *HysteriaUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.HysteriaUser{Name: user.Username, AuthString: user.Password}
	}
	i.postUpdate()
}

func (i *HysteriaUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
