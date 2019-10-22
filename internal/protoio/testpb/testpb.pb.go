// Code generated by protoc-gen-go. DO NOT EDIT.
// source: testpb.proto

package testpb

import (
	fmt "fmt"
	proto "github.com/golang/protobuf/proto"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion3 // please upgrade the proto package

type M struct {
	Content              string   `protobuf:"bytes,1,opt,name=content,proto3" json:"content,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *M) Reset()         { *m = M{} }
func (m *M) String() string { return proto.CompactTextString(m) }
func (*M) ProtoMessage()    {}
func (*M) Descriptor() ([]byte, []int) {
	return fileDescriptor_1b98c0ed33edeb52, []int{0}
}

func (m *M) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_M.Unmarshal(m, b)
}
func (m *M) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_M.Marshal(b, m, deterministic)
}
func (m *M) XXX_Merge(src proto.Message) {
	xxx_messageInfo_M.Merge(m, src)
}
func (m *M) XXX_Size() int {
	return xxx_messageInfo_M.Size(m)
}
func (m *M) XXX_DiscardUnknown() {
	xxx_messageInfo_M.DiscardUnknown(m)
}

var xxx_messageInfo_M proto.InternalMessageInfo

func (m *M) GetContent() string {
	if m != nil {
		return m.Content
	}
	return ""
}

func init() {
	proto.RegisterType((*M)(nil), "testpb.M")
}

func init() { proto.RegisterFile("testpb.proto", fileDescriptor_1b98c0ed33edeb52) }

var fileDescriptor_1b98c0ed33edeb52 = []byte{
	// 72 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0xe2, 0x29, 0x49, 0x2d, 0x2e,
	0x29, 0x48, 0xd2, 0x2b, 0x28, 0xca, 0x2f, 0xc9, 0x17, 0x62, 0x83, 0xf0, 0x94, 0x64, 0xb9, 0x18,
	0x7d, 0x85, 0x24, 0xb8, 0xd8, 0x93, 0xf3, 0xf3, 0x4a, 0x52, 0xf3, 0x4a, 0x24, 0x18, 0x15, 0x18,
	0x35, 0x38, 0x83, 0x60, 0xdc, 0x24, 0x36, 0xb0, 0x6a, 0x63, 0x40, 0x00, 0x00, 0x00, 0xff, 0xff,
	0xcb, 0x37, 0x9b, 0x8f, 0x3d, 0x00, 0x00, 0x00,
}
