package constant

import (
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/service/manager/constant"
)

type InboundManager interface {
	AddUserManager(inbound adapter.Inbound) error
	GetUserManager(tag string) (UserManager, bool)
	GetUserManagerTags() []string
}

type UserManager interface {
	UpdateUser(user C.User)
	UpdateUsers(users []C.User)
	DeleteUser(username string)
}
