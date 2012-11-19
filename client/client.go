package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ErrorContextf prefixes any error stored in err with text formatted
// according to the format specifier. If err does not contain an error,
// ErrorContextf does nothing.
func ErrorContextf(err *error, format string, args ...interface{}) {
	if *err != nil {
		*err = errors.New(fmt.Sprintf(format, args...) + ": " + (*err).Error())
	}
}

func getConfig(envVars ...string) (value string) {
	value = ""
	for _, v := range envVars {
		value = os.Getenv(v)
		if value != "" {
			break
		}
	}
	return
}

func GetEnvVars() (username, password, tenant, region, authUrl string) {
	username = getConfig("OS_USERNAME", "NOVA_USERNAME")
	password = getConfig("OS_PASSWORD", "NOVA_PASSWORD")
	tenant = getConfig("OS_TENANT_NAME", "NOVA_PROJECT_ID")
	region = getConfig("OS_REGION_NAME", "NOVA_REGION")
	authUrl = getConfig("OS_AUTH_URL")
	return
}

const (
	OS_API_TOKENS               = "/tokens"
	OS_API_FLAVORS              = "/flavors"
	OS_API_FLAVORS_DETAIL       = "/flavors/detail"
	OS_API_SERVERS              = "/servers"
	OS_API_SERVERS_DETAIL       = "/servers/detail"
	OS_API_SECURITY_GROUPS      = "/os-security-groups"
	OS_API_SECURITY_GROUP_RULES = "/os-security-group-rules"
	OS_API_FLOATING_IPS         = "/os-floating-ips"

	GET    = "GET"
	POST   = "POST"
	PUT    = "PUT"
	DELETE = "DELETE"
	HEAD   = "HEAD"
	COPY   = "COPY"
)

type endpoint struct {
	AdminURL    string
	Region      string
	InternalURL string
	Id          string
	PublicURL   string
}

type service struct {
	Name      string
	Type      string
	Endpoints []endpoint
}

type token struct {
	Expires string
	Id      string
	Tenant  struct {
		Enabled     bool
		Description string
		Name        string
		Id          string
	}
}

type user struct {
	Username string
	Roles    []struct {
		Name string
	}
	Id   string
	Name string
}

type metadata struct {
	IsAdmin bool
	Roles   []string
}

type OpenStackClient struct {
	// URL to the OpenStack Identity service (Keystone)
	IdentityEndpoint string
	// Which region to use
	Region string

	client *http.Client

	Services map[string]service
	Token    token
	User     user
	Metadata metadata
}

func (c *OpenStackClient) Authenticate(username, password, tenant string) (err error) {
	err = nil
	if username == "" || password == "" || tenant == "" {
		return fmt.Errorf("required argument(s) missing")
	}
	var req struct {
		Auth struct {
			Credentials struct {
				Username string `json:"username"`
				Password string `json:"password"`
			} `json:"passwordCredentials"`
			Tenant string `json:"tenantName"`
		} `json:"auth"`
	}
	req.Auth.Credentials.Username = username
	req.Auth.Credentials.Password = password
	req.Auth.Tenant = tenant

	var resp struct {
		Access struct {
			Token          token
			ServiceCatalog []service
			User           user
			Metadata       metadata
		}
	}

	err = c.request(POST, c.IdentityEndpoint+OS_API_TOKENS, nil, req, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "authentication failed")
		return
	}

	c.Token = resp.Access.Token
	c.User = resp.Access.User
	c.Metadata = resp.Access.Metadata
	if c.Services == nil {
		c.Services = make(map[string]service)
	}
	for _, s := range resp.Access.ServiceCatalog {
		// Filter endpoints outside our region
		for i, e := range s.Endpoints {
			if e.Region != c.Region {
				s.Endpoints = append(s.Endpoints[:i], s.Endpoints[i+1:]...)
			}
		}
		c.Services[s.Type] = s
	}
	return nil
}

func (c *OpenStackClient) IsAuthenticated() bool {
	return c.Token.Id != ""
}

type Link struct {
	Href string
	Rel  string
	Type string
}

// Entity can describe a flavor, flavor detail or server.
// Contains a list of links.
type Entity struct {
	Id    string
	Links []Link
	Name  string
}

func (c *OpenStackClient) ListFlavors() (flavors []Entity, err error) {

	var resp struct {
		Flavors []Entity
	}
	err = c.authRequest(GET, "compute", OS_API_FLAVORS, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to get list of flavors")
		return
	}

	return resp.Flavors, nil
}

type FlavorDetail struct {
	Name  string
	RAM   int
	VCPUs int
	Disk  int
	Id    string
	Swap  interface{} // Can be an empty string (?!)
}

func (c *OpenStackClient) ListFlavorsDetail() (flavors []FlavorDetail, err error) {

	var resp struct {
		Flavors []FlavorDetail
	}
	err = c.authRequest(GET, "compute", OS_API_FLAVORS_DETAIL, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to get list of flavors details")
		return
	}

	return resp.Flavors, nil
}

func (c *OpenStackClient) ListServers() (servers []Entity, err error) {

	var resp struct {
		Servers []Entity
	}
	err = c.authRequest(GET, "compute", OS_API_SERVERS, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to get list of servers")
		return
	}

	return resp.Servers, nil
}

type ServerDetail struct {
	AddressIPv4 string
	AddressIPv6 string
	Created     string
	Flavor      Entity
	HostId      string
	Id          string
	Image       Entity
	Links       []Link
	Name        string
	Progress    int
	Status      string
	TenantId    string `json:"tenant_id"`
	Updated     string
	UserId      string `json:"user_id"`
}

func (c *OpenStackClient) ListServersDetail() (servers []ServerDetail, err error) {

	var resp struct {
		Servers []ServerDetail
	}
	err = c.authRequest(GET, "compute", OS_API_SERVERS_DETAIL, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to get list of servers details")
		return
	}

	return resp.Servers, nil
}

func (c *OpenStackClient) GetServer(serverId string) (ServerDetail, error) {

	var resp struct {
		Server ServerDetail
	}
	url := fmt.Sprintf("%s/%s", OS_API_SERVERS, serverId)
	err := c.authRequest(GET, "compute", url, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to get details for serverId=%s", serverId)
		return ServerDetail{}, err
	}

	return resp.Server, nil
}

func (c *OpenStackClient) DeleteServer(serverId string) error {

	var resp struct {
		Server ServerDetail
	}
	url := fmt.Sprintf("%s/%s", OS_API_SERVERS, serverId)
	err := c.authRequest(DELETE, "compute", url, nil, nil, nil, &resp, []int{http.StatusNoContent}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to delete server with serverId=%s", serverId)
		return err
	}

	return nil
}

type RunServerOpts struct {
	Name               string  `json:"name"`
	FlavorId           string  `json:"flavorRef"`
	ImageId            string  `json:"imageRef"`
	UserData           *string `json:"user_data"`
	SecurityGroupNames []struct {
		Name string `json:"name"`
	} `json:"security_groups"`
}

func (c *OpenStackClient) RunServer(opts RunServerOpts) (err error) {

	var req struct {
		Server RunServerOpts `json:"server"`
	}
	req.Server = opts
	if opts.UserData != nil {
		data := []byte(*opts.UserData)
		encoded := base64.StdEncoding.EncodeToString(data)
		req.Server.UserData = &encoded
	}
	err = c.authRequest(POST, "compute", OS_API_SERVERS, nil, nil, &req, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to run a server with %#v", opts)
	}

	return
}

type SecurityGroupRule struct {
	FromPort      *int              `json:"from_port"`   // Can be nil
	IPProtocol    *string           `json:"ip_protocol"` // Can be nil
	ToPort        *int              `json:"to_port"`     // Can be nil
	ParentGroupId int               `json:"parent_group_id"`
	IPRange       map[string]string `json:"ip_range"` // Can be empty
	Id            int
	Group         map[string]string // Can be empty
}

type SecurityGroup struct {
	Rules       []SecurityGroupRule
	TenantId    string `json:"tenant_id"`
	Id          int
	Name        string
	Description string
}

func (c *OpenStackClient) ListSecurityGroups() (groups []SecurityGroup, err error) {

	var resp struct {
		Groups []SecurityGroup `json:"security_groups"`
	}
	err = c.authRequest(GET, "compute", OS_API_SECURITY_GROUPS, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to list security groups")
		return nil, err
	}

	return resp.Groups, nil
}

func (c *OpenStackClient) GetServerSecurityGroups(serverId string) (groups []SecurityGroup, err error) {

	var resp struct {
		Groups []SecurityGroup `json:"security_groups"`
	}
	url := fmt.Sprintf("%s/%s/%s", OS_API_SERVERS, serverId, OS_API_SECURITY_GROUPS)
	err = c.authRequest(GET, "compute", url, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to list server (%s) security groups", serverId)
		return nil, err
	}

	return resp.Groups, nil
}

func (c *OpenStackClient) CreateSecurityGroup(name, description string) (group SecurityGroup, err error) {

	var req struct {
		SecurityGroup struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"security_group"`
	}
	req.SecurityGroup.Name = name
	req.SecurityGroup.Description = description

	var resp struct {
		SecurityGroup SecurityGroup `json:"security_group"`
	}
	err = c.authRequest(POST, "compute", OS_API_SECURITY_GROUPS, nil, nil, &req, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to create a security group with name=%s", name)
	}
	group = resp.SecurityGroup

	return
}

func (c *OpenStackClient) DeleteSecurityGroup(groupId int) (err error) {

	url := fmt.Sprintf("%s/%d", OS_API_SECURITY_GROUPS, groupId)
	err = c.authRequest(DELETE, "compute", url, nil, nil, nil, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to delete a security group with id=%d", groupId)
	}

	return
}

type RuleInfo struct {
	IPProtocol    string `json:"ip_protocol"`     // Required, if GroupId is nil
	FromPort      int    `json:"from_port"`       // Required, if GroupId is nil
	ToPort        int    `json:"to_port"`         // Required, if GroupId is nil
	Cidr          string `json:"cidr"`            // Required, if GroupId is nil
	GroupId       *int   `json:"group_id"`        // If nil, FromPort/ToPort/IPProtocol must be set
	ParentGroupId int    `json:"parent_group_id"` // Required always
}

func (c *OpenStackClient) CreateSecurityGroupRule(ruleInfo RuleInfo) (rule SecurityGroupRule, err error) {

	var req struct {
		SecurityGroupRule RuleInfo `json:"security_group_rule"`
	}
	req.SecurityGroupRule = ruleInfo

	var resp struct {
		SecurityGroupRule SecurityGroupRule `json:"security_group_rule"`
	}

	err = c.authRequest(POST, "compute", OS_API_SECURITY_GROUP_RULES, nil, nil, &req, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to create a rule for the security group with id=%s", ruleInfo.GroupId)
	}

	return resp.SecurityGroupRule, err
}

func (c *OpenStackClient) DeleteSecurityGroupRule(ruleId int) (err error) {

	url := fmt.Sprintf("%s/%d", OS_API_SECURITY_GROUP_RULES, ruleId)
	err = c.authRequest(DELETE, "compute", url, nil, nil, nil, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to delete a security group rule with id=%d", ruleId)
	}

	return
}

func (c *OpenStackClient) AddServerSecurityGroup(serverId, groupName string) (err error) {

	var req struct {
		AddSecurityGroup struct {
			Name string `json:"name"`
		} `json:"addSecurityGroup"`
	}
	req.AddSecurityGroup.Name = groupName

	url := fmt.Sprintf("%s/%s/action", OS_API_SERVERS, serverId)
	err = c.authRequest(POST, "compute", url, nil, nil, &req, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to add security group '%s' from server with id=%s", groupName, serverId)
	}
	return
}

func (c *OpenStackClient) RemoveServerSecurityGroup(serverId, groupName string) (err error) {

	var req struct {
		RemoveSecurityGroup struct {
			Name string `json:"name"`
		} `json:"removeSecurityGroup"`
	}
	req.RemoveSecurityGroup.Name = groupName

	url := fmt.Sprintf("%s/%s/action", OS_API_SERVERS, serverId)
	err = c.authRequest(POST, "compute", url, nil, nil, &req, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to remove security group '%s' from server with id=%s", groupName, serverId)
	}
	return
}

type FloatingIP struct {
	FixedIP    interface{} `json:"fixed_ip"` // Can be a string or null
	Id         int         `json:"id"`
	InstanceId interface{} `json:"instance_id"` // Can be a string or null
	IP         string      `json:"ip"`
	Pool       string      `json:"pool"`
}

func (c *OpenStackClient) ListFloatingIPs() (ips []FloatingIP, err error) {

	var resp struct {
		FloatingIPs []FloatingIP `json:"floating_ips"`
	}

	err = c.authRequest(GET, "compute", OS_API_FLOATING_IPS, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to list floating ips")
	}

	return resp.FloatingIPs, err
}

func (c *OpenStackClient) GetFloatingIP(ipId int) (ip FloatingIP, err error) {

	var resp struct {
		FloatingIP FloatingIP `json:"floating_ip"`
	}

	url := fmt.Sprintf("%s/%d", OS_API_FLOATING_IPS, ipId)
	err = c.authRequest(GET, "compute", url, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to get floating ip %d details", ipId)
	}

	return resp.FloatingIP, err
}

func (c *OpenStackClient) AllocateFloatingIP() (ip FloatingIP, err error) {

	var resp struct {
		FloatingIP FloatingIP `json:"floating_ip"`
	}

	err = c.authRequest(POST, "compute", OS_API_FLOATING_IPS, nil, nil, nil, &resp, []int{http.StatusOK}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to allocate a floating ip")
	}

	return resp.FloatingIP, err
}

func (c *OpenStackClient) DeleteFloatingIP(ipId int) (err error) {

	url := fmt.Sprintf("%s/%d", OS_API_FLOATING_IPS, ipId)
	err = c.authRequest(DELETE, "compute", url, nil, nil, nil, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to delete floating ip %d details", ipId)
	}

	return
}

func (c *OpenStackClient) AddServerFloatingIP(serverId, address string) (err error) {

	var req struct {
		AddFloatingIP struct {
			Address string `json:"address"`
		} `json:"addFloatingIp"`
	}
	req.AddFloatingIP.Address = address

	url := fmt.Sprintf("%s/%s/action", OS_API_SERVERS, serverId)
	err = c.authRequest(POST, "compute", url, nil, nil, &req, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to add floating ip %s to server %s", address, serverId)
	}

	return
}

func (c *OpenStackClient) RemoveServerFloatingIP(serverId, address string) (err error) {

	var req struct {
		RemoveFloatingIP struct {
			Address string `json:"address"`
		} `json:"removeFloatingIp"`
	}
	req.RemoveFloatingIP.Address = address

	url := fmt.Sprintf("%s/%s/action", OS_API_SERVERS, serverId)
	err = c.authRequest(POST, "compute", url, nil, nil, &req, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to remove floating ip %s to server %s", address, serverId)
	}

	return
}

func (c *OpenStackClient) CreateContainer(containerName string) (err error) {

	// Juju expects there to be a (semi) public url for some objects. This
	// could probably be more restrictive or placed in a seperate container
	// with some refactoring, but for now just make everything public.
	headers := make(http.Header)
	headers.Add("X-Container-Read", ".r:*")
	url := fmt.Sprintf("/%s", containerName)
	err = c.authRequest(PUT, "object-store", url, nil, nil, nil, nil, []int{http.StatusAccepted, http.StatusCreated}, headers, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to create container %s.", containerName)
	}

	return
}

func (c *OpenStackClient) DeleteContainer(containerName string) (err error) {

	url := fmt.Sprintf("/%s", containerName)
	err = c.authRequest(DELETE, "object-store", url, nil, nil, nil, nil, []int{http.StatusNoContent}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to delete container %s.", containerName)
	}

	return
}

func (c *OpenStackClient) PublicObjectURL(containerName, objectName string) (url string, err error) {
	path := fmt.Sprintf("/%s/%s", containerName, objectName)
	return c.makeUrl("object-store", []string{path}, nil)
}

func (c *OpenStackClient) HeadObject(containerName, objectName string) (headers http.Header, err error) {

	url, err := c.PublicObjectURL(containerName, objectName)
	if err != nil {
		return nil, err
	}
	err = c.authRequest(HEAD, "object-store", url, nil, nil, nil, nil, []int{http.StatusOK}, nil, &headers, nil)
	if err != nil {
		ErrorContextf(&err, "failed to HEAD object %s from container %s", objectName, containerName)
		return nil, err
	}
	return headers, nil
}

func (c *OpenStackClient) GetObject(containerName, objectName string) (obj []byte, err error) {

	url, err := c.PublicObjectURL(containerName, objectName)
	if err != nil {
		return nil, err
	}
	err = c.authRequest(GET, "object-store", url, nil, nil, nil, nil, []int{http.StatusOK}, nil, nil, &obj)
	if err != nil {
		ErrorContextf(&err, "failed to GET object %s content from container %s", objectName, containerName)
		return nil, err
	}
	return obj, nil
}

func (c *OpenStackClient) DeleteObject(containerName, objectName string) (err error) {

	url, err := c.PublicObjectURL(containerName, objectName)
	if err != nil {
		return err
	}
	err = c.authRequest(DELETE, "object-store", url, nil, nil, nil, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to DELETE object %s content from container %s", objectName, containerName)
	}
	return err
}

func (c *OpenStackClient) PutObject(containerName, objectName string, data []byte) (err error) {

	url, err := c.PublicObjectURL(containerName, objectName)
	if err != nil {
		return err
	}
	err = c.authRequest(PUT, "object-store", url, nil, data, nil, nil, []int{http.StatusAccepted}, nil, nil, nil)
	if err != nil {
		ErrorContextf(&err, "failed to PUT object %s content from container %s", objectName, containerName)
	}
	return err
}

////////////////////////////////////////////////////////////////////////
// Private helpers

// request sends an HTTP request with the given method to the given URL,
// containing an optional body (serialized to JSON), and returning either
// an error or the (deserialized) response body
func (c *OpenStackClient) request(method, url string, rawReqBody []byte, body, resp interface{}, expectedStatus []int, reqHeaders http.Header, respHeaders *http.Header, rawRespBody *[]byte) (err error) {
	err = nil
	if c.client == nil {
		c.client = &http.Client{CheckRedirect: nil}
	}

	var (
		req      *http.Request
		jsonBody []byte
	)
	if rawReqBody != nil {
		rawReqReader := bytes.NewReader(rawReqBody)
		req, err = http.NewRequest(method, url, rawReqReader)
	} else if body != nil {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			ErrorContextf(&err, "failed marshalling the request body")
			return
		}

		reqBody := strings.NewReader(string(jsonBody))
		req, err = http.NewRequest(method, url, reqBody)
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		ErrorContextf(&err, "failed creating the request")
		return
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	if reqHeaders != nil {
		for header, values := range reqHeaders {
			for _, value := range values {
				req.Header.Add(header, value)
			}
		}
	}
	if c.IsAuthenticated() {
		req.Header.Add("X-Auth-Token", c.Token.Id)
	}

	rawResp, err := c.client.Do(req)
	if err != nil {
		ErrorContextf(&err, "failed executing the request")
		return
	}
	foundStatus := false
	for _, status := range expectedStatus {
		if rawResp.StatusCode == status {
			foundStatus = true
			break
		}
	}
	if !foundStatus && len(expectedStatus) > 0 {
		defer rawResp.Body.Close()
		respBody, _ := ioutil.ReadAll(rawResp.Body)
		err = errors.New(
			fmt.Sprintf(
				"request (%s) returned unexpected status: %s; response body: %s; request body: %s",
				url,
				rawResp.Status,
				respBody,
				string(jsonBody)))
		return
	}

	respBody, err := ioutil.ReadAll(rawResp.Body)
	defer rawResp.Body.Close()
	if err != nil {
		ErrorContextf(&err, "failed reading the response body")
		return
	}

	if len(respBody) > 0 {
		if rawRespBody != nil {
			*rawRespBody = respBody
		} else if resp != nil {
			// resp and rawRespBody are mutually exclusive
			err = json.Unmarshal(respBody, &resp)
			if err != nil {
				ErrorContextf(&err, "failed unmarshaling the response body: %s", respBody)
			}
		}
		if respHeaders != nil {
			*respHeaders = rawResp.Header
		}
	} else {
		resp = nil
	}

	return
}

// makeUrl prepares a full URL to a service endpoint, with optional
// URL parts, appended to it and optional query string params. It
// uses the first endpoint it can find for the given service type
func (c *OpenStackClient) makeUrl(serviceType string, parts []string, params url.Values) (string, error) {
	s, ok := c.Services[serviceType]
	if !ok || len(s.Endpoints) == 0 {
		return "", errors.New("no endpoints known for service type: " + serviceType)
	}
	url := s.Endpoints[0].PublicURL
	for _, part := range parts {
		url += part
	}
	if params != nil {
		url += "?" + params.Encode()
	}
	return url, nil
}

func (c *OpenStackClient) authRequest(method, svcType, apiCall string, params url.Values, rawBody []byte, body, resp interface{}, expectedStatus []int, reqHeaders http.Header, respHeaders *http.Header, respRawBody *[]byte) (err error) {

	if !c.IsAuthenticated() {
		return errors.New("not authenticated")
	}

	url, err := c.makeUrl(svcType, []string{apiCall}, params)
	if err != nil {
		ErrorContextf(&err, "cannot find a '%s' node endpoint", svcType)
		return
	}

	if body != nil && resp != nil {
		err = c.request(method, url, rawBody, &body, &resp, expectedStatus, reqHeaders, respHeaders, respRawBody)
	} else if resp != nil {
		err = c.request(method, url, rawBody, nil, &resp, expectedStatus, reqHeaders, respHeaders, respRawBody)
	} else if body != nil {
		err = c.request(method, url, rawBody, &body, nil, expectedStatus, reqHeaders, respHeaders, respRawBody)
	}
	if err != nil {
		ErrorContextf(&err, "request failed")
	}
	return
}