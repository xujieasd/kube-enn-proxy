package proxy

import (
	"k8s.io/apimachinery/pkg/types"
)

type ServicePortName struct {
	types.NamespacedName
	Port string
}

