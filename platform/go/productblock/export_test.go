package productblock

import "time"

func SetClock(c *Client, now func() time.Time) { c.now = now }
