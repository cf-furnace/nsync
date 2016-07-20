package helpers

func Int32Ptr(i int) *int32 {
	i32 := int32(i)
	return &i32
}
