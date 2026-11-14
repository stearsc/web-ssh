package fault

// Error 只携带公开错误码，底层错误不得直接进入 HTTP 或日志。
type Error string

func (e Error) Error() string { return string(e) }
