package ipvs

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"


	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
	"github.com/docker/libnetwork/ipvs"

)

type Proxier struct {

	mu	sync.Mutex

}

func NewProxier()(*Proxier, error){

}

func (proxier *Proxier) Sync(){

}

func (proxier *Proxier) syncProxyRules(){

}
