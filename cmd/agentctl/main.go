// Command agentctl is the narrow helper named by the agent account's sudoers
// rule on Linux.
package main

import "fmt"

var version = "dev"

func main() {
	fmt.Println(version)
}
