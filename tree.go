// Copyright (C) 2015 Alex Sergeyev
// This project is licensed under the terms of the MIT license.
// Read LICENSE file for information for all notices and permissions.

package nradix

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
)

type node struct {
	left, right, parent *node
	value               interface{}
}

// Tree implements radix tree for working with IP/mask. Thread safety is not guaranteed, you should choose your own style of protecting safety of operations.
type Tree struct {
	root *node
	free *node

	alloc []node
}

const (
	startbit  = uint32(0x80000000)
	startbyte = byte(0x80)
)

var (
	ErrNodeBusy = errors.New("Node Busy")
	ErrNotFound = errors.New("No Such Node")
	ErrBadIP    = errors.New("Bad IP address or mask")
)

// NewTree creates Tree and preallocates (if preallocate not zero) number of nodes that would be ready to fill with data.
func NewTree(preallocate int) *Tree {
	tree := new(Tree)
	tree.root = tree.newnode()
	if preallocate == 0 {
		return tree
	}

	// Simplification, static preallocate max 6 bits
	if preallocate > 6 || preallocate < 0 {
		preallocate = 6
	}

	var key, mask uint32

	for inc := startbit; preallocate > 0; inc, preallocate = inc>>1, preallocate-1 {
		key = 0
		mask >>= 1
		mask |= startbit

		for {
			tree.insert32(key, mask, nil, false)
			key += inc
			if key == 0 { // magic bits collide
				break
			}
		}
	}

	return tree
}

// AddCIDR adds value associated with IP/mask to the tree. Will return error for invalid CIDR or if value already exists.
func (tree *Tree) AddCIDR(cidr string, val interface{}) error {
	return tree.AddCIDRb([]byte(cidr), val)
}

func (tree *Tree) AddCIDRb(cidr []byte, val interface{}) error {
	ip, mask, err := parseCIDR(cidr)
	if err != nil {
		return err
	}

	if len(ip) == net.IPv4len {
		ipInteger := binary.BigEndian.Uint32(ip)
		maskInteger := binary.BigEndian.Uint32(mask)
		return tree.insert32(ipInteger, maskInteger, val, false)
	}

	return tree.insert(ip, mask, val, false)
}

// AddCIDR adds value associated with IP/mask to the tree. Will return error for invalid CIDR or if value already exists.
func (tree *Tree) SetCIDR(cidr string, val interface{}) error {
	return tree.SetCIDRb([]byte(cidr), val)
}

func (tree *Tree) SetCIDRb(cidr []byte, val interface{}) error {
	ip, mask, err := parseCIDR(cidr)
	if err != nil {
		return err
	}

	if len(ip) == net.IPv4len {
		ipInteger := binary.BigEndian.Uint32(ip)
		maskInteger := binary.BigEndian.Uint32(mask)
		return tree.insert32(ipInteger, maskInteger, val, true)
	}

	return tree.insert(ip, mask, val, true)
}

// DeleteWholeRangeCIDR removes all values associated with IPs
// in the entire subnet specified by the CIDR.
func (tree *Tree) DeleteWholeRangeCIDR(cidr string) error {
	return tree.DeleteWholeRangeCIDRb([]byte(cidr))
}

func (tree *Tree) DeleteWholeRangeCIDRb(cidr []byte) error {
	ip, mask, err := parseCIDR(cidr)
	if err != nil {
		return err
	}

	if len(ip) == net.IPv4len {
		ipInteger := binary.BigEndian.Uint32(ip)
		maskInteger := binary.BigEndian.Uint32(mask)
		return tree.delete32(ipInteger, maskInteger, true)
	}

	return tree.delete(ip, mask, true)
}

// DeleteCIDR removes value associated with IP/mask from the tree.
func (tree *Tree) DeleteCIDR(cidr string) error {
	return tree.DeleteCIDRb([]byte(cidr))
}

func (tree *Tree) DeleteCIDRb(cidr []byte) error {
	ip, mask, err := parseCIDR(cidr)
	if err != nil {
		return err
	}

	if len(ip) == net.IPv4len {
		ipInteger := binary.BigEndian.Uint32(ip)
		maskInteger := binary.BigEndian.Uint32(mask)
		return tree.delete32(ipInteger, maskInteger, false)
	}

	return tree.delete(ip, mask, false)
}

// Find CIDR traverses tree to proper Node and returns previously saved information in longest covered IP.
func (tree *Tree) FindCIDR(cidr string) (interface{}, error) {
	return tree.FindCIDRb([]byte(cidr))
}

func (tree *Tree) FindCIDRb(cidr []byte) (interface{}, error) {
	ip, mask, err := parseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	if len(ip) == net.IPv4len {
		ipInteger := binary.BigEndian.Uint32(ip)
		maskInteger := binary.BigEndian.Uint32(mask)
		return tree.find32(ipInteger, maskInteger), nil
	}

	return tree.find(ip, mask), nil
}

func (tree *Tree) insert32(key, mask uint32, value interface{}, overwrite bool) error {
	bit := startbit
	node := tree.root
	next := tree.root
	for bit&mask != 0 {
		if key&bit != 0 {
			next = node.right
		} else {
			next = node.left
		}
		if next == nil {
			break
		}
		bit = bit >> 1
		node = next
	}
	if next != nil {
		if node.value != nil && !overwrite {
			return ErrNodeBusy
		}
		node.value = value
		return nil
	}
	for bit&mask != 0 {
		next = tree.newnode()
		next.parent = node
		if key&bit != 0 {
			node.right = next
		} else {
			node.left = next
		}
		bit >>= 1
		node = next
	}
	node.value = value

	return nil
}

func (tree *Tree) insert(key net.IP, mask net.IPMask, value interface{}, overwrite bool) error {
	if len(key) != len(mask) {
		return ErrBadIP
	}

	var i int
	bit := startbyte
	node := tree.root
	next := tree.root
	for bit&mask[i] != 0 {
		if key[i]&bit != 0 {
			next = node.right
		} else {
			next = node.left
		}
		if next == nil {
			break
		}

		node = next

		if bit >>= 1; bit == 0 {
			if i++; i == len(key) {
				break
			}
			bit = startbyte
		}

	}
	if next != nil {
		if node.value != nil && !overwrite {
			return ErrNodeBusy
		}
		node.value = value
		return nil
	}

	for bit&mask[i] != 0 {
		next = tree.newnode()
		next.parent = node
		if key[i]&bit != 0 {
			node.right = next
		} else {
			node.left = next
		}
		node = next
		if bit >>= 1; bit == 0 {
			if i++; i == len(key) {
				break
			}
			bit = startbyte
		}
	}
	node.value = value

	return nil
}

func (tree *Tree) delete32(key, mask uint32, wholeRange bool) error {
	bit := startbit
	node := tree.root
	for node != nil && bit&mask != 0 {
		if key&bit != 0 {
			node = node.right
		} else {
			node = node.left
		}
		bit >>= 1
	}
	if node == nil {
		return ErrNotFound
	}

	if !wholeRange && (node.right != nil || node.left != nil) {
		// keep it just trim value
		if node.value != nil {
			node.value = nil
			return nil
		}
		return ErrNotFound
	}

	// need to trim leaf
	for {
		if node.parent.right == node {
			node.parent.right = nil
		} else {
			node.parent.left = nil
		}
		// reserve this node for future use
		node.right = tree.free
		tree.free = node
		// move to parent, check if it's free of value and children
		node = node.parent
		if node.right != nil || node.left != nil || node.value != nil {
			break
		}
		// do not delete root node
		if node.parent == nil {
			break
		}
	}

	return nil
}

func (tree *Tree) delete(key net.IP, mask net.IPMask, wholeRange bool) error {
	if len(key) != len(mask) {
		return ErrBadIP
	}

	var i int
	bit := startbyte
	node := tree.root
	for node != nil && bit&mask[i] != 0 {
		if key[i]&bit != 0 {
			node = node.right
		} else {
			node = node.left
		}
		if bit >>= 1; bit == 0 {
			if i++; i == len(key) {
				break
			}
			bit = startbyte
		}
	}
	if node == nil {
		return ErrNotFound
	}

	if !wholeRange && (node.right != nil || node.left != nil) {
		// keep it just trim value
		if node.value != nil {
			node.value = nil
			return nil
		}
		return ErrNotFound
	}

	// need to trim leaf
	for {
		if node.parent.right == node {
			node.parent.right = nil
		} else {
			node.parent.left = nil
		}
		// reserve this node for future use
		node.right = tree.free
		tree.free = node

		// move to parent, check if it's free of value and children
		node = node.parent
		if node.right != nil || node.left != nil || node.value != nil {
			break
		}
		// do not delete root node
		if node.parent == nil {
			break
		}
	}

	return nil
}

func (tree *Tree) find32(key, mask uint32) (value interface{}) {
	bit := startbit
	node := tree.root
	for node != nil {
		if node.value != nil {
			value = node.value
		}
		if key&bit != 0 {
			node = node.right
		} else {
			node = node.left
		}
		if mask&bit == 0 {
			break
		}
		bit >>= 1

	}
	return value
}

func (tree *Tree) find(key net.IP, mask net.IPMask) (value interface{}) {
	if len(key) != len(mask) {
		return ErrBadIP
	}
	var i int
	bit := startbyte
	node := tree.root
	for node != nil {
		if node.value != nil {
			value = node.value
		}
		if key[i]&bit != 0 {
			node = node.right
		} else {
			node = node.left
		}
		if mask[i]&bit == 0 {
			break
		}
		if bit >>= 1; bit == 0 {
			i, bit = i+1, startbyte
			if i >= len(key) {
				// reached depth of the tree, there should be matching node...
				if node != nil {
					value = node.value
				}
				break
			}
		}
	}
	return value
}

func (tree *Tree) newnode() (p *node) {
	if tree.free != nil {
		p = tree.free
		tree.free = tree.free.right

		// release all prior links
		p.right = nil
		p.parent = nil
		p.left = nil
		p.value = nil
		return p
	}

	ln := len(tree.alloc)
	if ln == cap(tree.alloc) {
		// filled one row, make bigger one
		tree.alloc = make([]node, ln+200)[:1] // 200, 600, 1400, 3000, 6200, 12600 ...
		ln = 0
	} else {
		tree.alloc = tree.alloc[:ln+1]
	}
	return &(tree.alloc[ln])
}

// parseCIDR converts a string address or network prefix to a prefix in memory represented by a net.IP and net.IPMask.
// Parsed IPs will be returned as single-address prefixes. It is also notable that all IPv4-mapped IPv6 addresses and
// prefixes will be converted to their IPv4 counterparts to prevent multiple distinct instances of a single key from
// being created. In the event that parsing can not be completed, a non-nil error will be returned instead.
func parseCIDR(cidr []byte) (net.IP, net.IPMask, error) {
	var address netip.Addr
	var prefixLength int

	// Check for '/' to determine if this is a single IP or prefix. This is the same approach used by net.ParseCIDR
	if bytes.IndexByte(cidr, '/') < 0 {
		// netip.ParseAddr is used instead of net.ParseIP since net.ParseIP reads all IPv4 addresses as IPv4-mapped IPv6
		// addresses and the net package lacks the functionality to convert them back
		addr, err := netip.ParseAddr(string(cidr))

		if err != nil {
			return nil, nil, err
		}

		address = addr
		prefixLength = address.BitLen()
	} else {
		prefix, err := netip.ParsePrefix(string(cidr))

		if err != nil {
			return nil, nil, err
		}

		address = prefix.Addr()
		prefixLength = prefix.Bits()
	}

	// Check for IPv4-mapped IPv6 addresses and convert them to IPv4 by dropping the first 12 bytes of the IPv6 prefix
	// Ex: ::ffff:1.2.3.4/112 -> 1.2.3.4/16
	if address.Is4In6() {
		address = address.Unmap()
		prefixLength -= 8 * (net.IPv6len - net.IPv4len)

		if prefixLength < 0 {
			return nil, nil, ErrBadIP
		}
	}

	return address.AsSlice(), net.CIDRMask(prefixLength, address.BitLen()), nil
}
