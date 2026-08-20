package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/vless"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type VLESSManager struct {
	inbounds map[string]*VLESSUserManager

	mtx sync.Mutex
}

func NewVLESSManager() *VLESSManager {
	return &VLESSManager{
		inbounds: make(map[string]*VLESSUserManager),
	}
}

func (m *VLESSManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &VLESSUserManager{
		inbound:  inbound.(*vless.Inbound),
		usersMap: make(map[string]option.VLESSUser),
	}
	return nil
}

func (m *VLESSManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *VLESSManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type VLESSUserManager struct {
	inbound  *vless.Inbound
	usersMap map[string]option.VLESSUser

	mtx sync.Mutex
}

func (i *VLESSUserManager) postUpdate() {
	users := make([]option.VLESSUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *VLESSUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.VLESSUser{Name: user.Username, UUID: user.UUID, Flow: user.Flow}
	i.postUpdate()
}

func (i *VLESSUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.VLESSUser{Name: user.Username, UUID: user.UUID, Flow: user.Flow}
	}
	i.postUpdate()
}

func (i *VLESSUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
