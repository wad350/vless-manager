package inbound

import (
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/vmess"
	CM "github.com/sagernet/sing-box/service/manager/constant"
	"github.com/sagernet/sing-box/service/node/constant"
)

type VMessManager struct {
	inbounds map[string]*VMessUserManager

	mtx sync.Mutex
}

func NewVMessManager() *VMessManager {
	return &VMessManager{
		inbounds: make(map[string]*VMessUserManager),
	}
}

func (m *VMessManager) AddUserManager(inbound adapter.Inbound) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.inbounds[inbound.Tag()] = &VMessUserManager{
		inbound:  inbound.(*vmess.Inbound),
		usersMap: make(map[string]option.VMessUser),
	}
	return nil
}

func (m *VMessManager) GetUserManager(tag string) (constant.UserManager, bool) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	inbound, ok := m.inbounds[tag]
	return inbound, ok
}

func (m *VMessManager) GetUserManagerTags() []string {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	tags := make([]string, 0, len(m.inbounds))
	for tag := range m.inbounds {
		tags = append(tags, tag)
	}
	return tags
}

type VMessUserManager struct {
	inbound  *vmess.Inbound
	usersMap map[string]option.VMessUser

	mtx sync.Mutex
}

func (i *VMessUserManager) postUpdate() {
	users := make([]option.VMessUser, 0, len(i.usersMap))
	for _, user := range i.usersMap {
		users = append(users, user)
	}
	i.inbound.UpdateUsers(users)
}

func (i *VMessUserManager) UpdateUser(user CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.usersMap[user.Username] = option.VMessUser{Name: user.Username, UUID: user.UUID, AlterId: user.AlterID}
	i.postUpdate()
}

func (i *VMessUserManager) UpdateUsers(users []CM.User) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	clear(i.usersMap)
	for _, user := range users {
		i.usersMap[user.Username] = option.VMessUser{Name: user.Username, UUID: user.UUID, AlterId: user.AlterID}
	}
	i.postUpdate()
}

func (i *VMessUserManager) DeleteUser(username string) {
	i.mtx.Lock()
	defer i.mtx.Unlock()
	delete(i.usersMap, username)
	i.postUpdate()
}
