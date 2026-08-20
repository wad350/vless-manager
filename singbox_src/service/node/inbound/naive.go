package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/protocol/naive"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
	"github.com/sagernet/sing/common/auth"
)

type NaiveManager struct {
	inbounds map[string]*NaiveUserManager

	mtx sync.Mutex
}

func NewNaiveManager() *NaiveManager {
	return &NaiveManager{
		inbounds: make(map[string]*NaiveUserManager),
	}
}

func (m *NaiveManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &NaiveUserManager{
		inbound:  inbound.(*naive.Inbound),
		usersMap: make(map[string]auth.User),
	}
	return nil
}

func (m *NaiveManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *NaiveManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type NaiveUserManager struct {
	inbound  *naive.Inbound
	usersMap map[string]auth.User

	mtx sync.Mutex
}

func (i *NaiveUserManager) postUpdate() {
	users := make([]auth.User, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *NaiveUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = auth.User{Username: user.Username, Password: user.Password}
	i.postUpdate()
}

func (i *NaiveUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = auth.User{Username: user.Username, Password: user.Password}
	}
	i.postUpdate()
}

func (i *NaiveUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
