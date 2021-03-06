package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/olekukonko/tablewriter"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/winglq/l4proxy/src/api"
	"github.com/winglq/l4proxy/src/handler"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type ClientOptions struct {
	svrAddr string
	pubPort int32
	intPort int32
	name    string
}

type ServerOptions struct {
	ctlAddr string
	host    string
}

type Options struct {
	s ServerOptions
	c ClientOptions
}

var opt Options
var cSig = make(chan os.Signal)

func init() {
	signal.Notify(cSig, os.Interrupt)
	log.SetLevel(log.DebugLevel)
}

func newLANCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "lan [host] [port]",
		Run: func(cmd *cobra.Command, args []string) {
			port := "22"
			host := "127.0.0.1"
			if len(args) > 0 {
				host = args[0]
			}
			if len(args) > 1 {
				port = args[1]
			}
			proxy(":222", fmt.Sprintf("%s:%s", host, port))
		},
	}
	return cmd
}

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "server",
		Run: func(cmd *cobra.Command, args []string) {
			ctlLtn, err := net.Listen("tcp", opt.s.ctlAddr)
			if err != nil {
				panic(err)
			}
			log.Printf("listen on ctl addr %s", ctlLtn.Addr())
			grpcServer := grpc.NewServer()
			go func() {
				<-cSig
				grpcServer.Stop()
			}()
			h := handler.New(opt.s.host)
			api.RegisterControlServiceServer(grpcServer, h)
			err = grpcServer.Serve(ctlLtn)
			if err != nil {
				panic(err)
			}

		},
	}
	cmd.Flags().StringVar(&opt.s.ctlAddr, "ctl_addr", ":2222", "server address")
	cmd.Flags().StringVar(&opt.s.host, "host", "127.0.0.1", "public host ip address or hostname")
	return cmd
}

func newClientCmd() *cobra.Command {
	cmd := cobra.Command{
		Use:  "client [host] [port]",
		Args: cobra.MaximumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			port := "22"
			host := "127.0.0.1"
			if len(args) > 0 {
				host = args[0]
			}
			if len(args) > 1 {
				port = args[1]
			}
			c, err := grpc.Dial(opt.c.svrAddr, grpc.WithInsecure())
			if err != nil {
				panic(err)
			}
			go func() {
				<-cSig
				c.Close()
			}()
			client := api.NewControlServiceClient(c)
			conClient, err := client.CreateClient(context.TODO(), &api.CreateClientRequest{
				DisplayName:  opt.c.name,
				PublicPort:   opt.c.pubPort,
				InternalPort: opt.c.intPort,
			})
			if err != nil {
				panic(err)
			}
			var wg sync.WaitGroup
			for {
				resp, err := conClient.Recv()
				if err != nil {
					if grpc.Code(err) == codes.Canceled {
						return
					} else if grpc.Code(err) == codes.Unavailable {
						log.Printf("connection closed: %v", err)
						return
					} else {
						panic(err)
					}
				}
				if resp.InternalAddress != "" {
					c, err := net.Dial("tcp", resp.InternalAddress)
					if err != nil {
						panic(err)
					}
					_, err = c.Write([]byte(resp.Token))
					if err != nil {
						panic(err)
					}
					sconn, err := net.Dial("tcp", fmt.Sprintf("%s:%s", host, port))
					if err != nil {
						panic(err)
					}
					log.Printf("connected to server: %s -> %s", sconn.LocalAddr(), sconn.RemoteAddr())
					pair := &handler.PairedConn{
						SRC:  c,
						DEST: sconn,
					}
					pair.Copy(&wg)
				} else {
					fmt.Printf("PUBLIC ADDRESS: %s\n", resp.PublicAddress)
				}
			}
			wg.Wait()
		},
	}
	cmd.PersistentFlags().StringVar(&opt.c.svrAddr, "svr_addr", "127.0.0.1:2222", "server address.")
	cmd.Flags().Int32Var(&opt.c.pubPort, "pub_port", 0, "public port for this client.")
	cmd.Flags().Int32Var(&opt.c.intPort, "int_port", 0, "internal port used to listen client connection.")
	cmd.Flags().StringVar(&opt.c.name, "client_name", "unknown", "client name")
	list := newListClientsCmd()
	cmd.AddCommand(list)
	return &cmd
}

func newListClientsCmd() *cobra.Command {
	cmd := cobra.Command{
		Use: "list",
		Run: func(cmd *cobra.Command, args []string) {
			c, err := grpc.Dial(opt.c.svrAddr, grpc.WithInsecure())
			if err != nil {
				panic(err)
			}
			client := api.NewControlServiceClient(c)
			resp, err := client.ListClients(context.TODO(), &api.ListClientsRequest{})
			if err != nil {
				fmt.Printf("list clients failed: %v", err)
				os.Exit(1)
			}
			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"Name", "Display Name", "Public Address", "Internal Address"})
			for _, cl := range resp.Clients {
				table.Append([]string{cl.Name, cl.DisplayName, cl.PublicAddress, cl.InternalAddress})

			}
			table.Render()
		},
	}
	list := newListClientUsersCmd()
	cmd.AddCommand(list)
	return &cmd
}

func newListClientUsersCmd() *cobra.Command {
	parent := ""
	cmd := cobra.Command{
		Use: "user",
		Run: func(cmd *cobra.Command, args []string) {
			if parent == "" {
				fmt.Println("client_name is reqired")
				return
			}
			c, err := grpc.Dial(opt.c.svrAddr, grpc.WithInsecure())
			if err != nil {
				panic(err)
			}
			client := api.NewControlServiceClient(c)
			resp, err := client.ListBackendServiceUsers(context.TODO(), &api.ListBackendServiceUsersRequest{
				Parent: parent,
			})
			if err != nil {
				fmt.Printf("list clients users failed: %v", err)
				os.Exit(1)
			}
			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"User Address"})
			for _, u := range resp.Users {
				table.Append([]string{u.UserAddr})

			}
			table.Render()
		},
	}
	cmd.Flags().StringVar(&parent, "client_name", "", "client unique name.")
	return &cmd
}

func newCmd() *cobra.Command {
	client := newClientCmd()
	svr := newServerCmd()
	lan := newLANCmd()
	cmd := &cobra.Command{
		Use:  "l4proxy",
		Long: "reverse proxy",
	}
	cmd.AddCommand(client, svr, lan)
	return cmd
}

func proxy(localAddr, remoteAddr string) {
	l, err := net.Listen("tcp", localAddr)
	if err != nil {
		panic(err)
	}
	go func() {
		<-cSig
		l.Close()
	}()
	log.Printf("listen on %s", l.Addr())
	var wg sync.WaitGroup
	for {
		cc, err := l.Accept()
		if err != nil && strings.Contains(err.Error(), "use of closed network connection") {
			break
		} else if err != nil {
			panic(err)
		}
		c, err := net.Dial("tcp", remoteAddr)
		if err != nil {
			panic(err)
		}
		log.Printf("dialed to %s", c.RemoteAddr())
		if err != nil {
			panic(err)
		}
		log.Printf("connected from %s", cc.RemoteAddr())
		p := &handler.PairedConn{
			SRC:  cc,
			DEST: c,
		}
		p.Copy(&wg)
		go func() {
			<-cSig
			p.Close()
		}()
	}
	wg.Wait()
}

func main() {
	cmd := newCmd()
	if err := cmd.Execute(); err != nil {
		panic(err)
	}
}
