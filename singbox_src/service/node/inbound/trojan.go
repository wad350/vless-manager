package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/trojan"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type TrojanManager struct {
	inbounds map[string]*TrojanUserManager

	mtx sync.Mutex
}

func NewTrojanManager() *TrojanManager {
	return &TrojanManager{
		inbounds: make(map[string]*TrojanUserManager),
	}
}

func (m *TrojanManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &TrojanUserManager{
		inbound:  inbound.(*trojan.Inbound),
		usersMap: make(map[string]option.TrojanUser),
	}
	return nil
}

func (m *TrojanManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *TrojanManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type TrojanUserManager struct {
	inbound  *trojan.Inbound
	usersMap map[string]option.TrojanUser

	mtx sync.Mutex
}

func (i *TrojanUserManager) postUpdate() {
	users := make([]option.TrojanUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *TrojanUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.TrojanUser{Name: user.Username, Password: user.Password}
	i.postUpdate()
}

func (i *TrojanUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.TrojanUser{Name: user.Username, Password: user.Password}
	}
	i.postUpdate()
}

func (i *TrojanUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
