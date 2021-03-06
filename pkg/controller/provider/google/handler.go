/*
 * Copyright 2019 SAP SE or an SAP affiliate company. All rights reserved. This file is licensed under the Apache Software License, v. 2 except as noted otherwise in the LICENSE file
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 *
 */

package google

import (
	"context"
	"fmt"
	"github.com/gardener/external-dns-management/pkg/dns/provider"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/gardener/controller-manager-library/pkg/logger"
	"github.com/gardener/external-dns-management/pkg/dns"

	googledns "google.golang.org/api/dns/v1"
)

type Handler struct {
	config      provider.DNSHandlerConfig
	credentials *google.Credentials
	client      *http.Client
	ctx         context.Context
	metrics     provider.Metrics
	service     *googledns.Service
}

var _ provider.DNSHandler = &Handler{}

func NewHandler(logger logger.LogContext, config *provider.DNSHandlerConfig, metrics provider.Metrics) (provider.DNSHandler, error) {
	var err error

	this := &Handler{
		config:  *config,
		metrics: metrics,
	}
	scopes := []string{
		//	"https://www.googleapis.com/auth/compute",
		//	"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/ndev.clouddns.readwrite",
		//	"https://www.googleapis.com/auth/devstorage.full_control",
	}

	json := this.config.Properties["serviceaccount.json"]
	if json == "" {
		return nil, fmt.Errorf("'serviceaccount.json' required in secret")
	}

	//c:=*http.DefaultClient
	//this.ctx=context.WithValue(config.Context,oauth2.HTTPClient,&c)
	this.ctx = config.Context

	this.credentials, err = google.CredentialsFromJSON(this.ctx, []byte(json), scopes...)
	//cfg, err:=google.JWTConfigFromJSON([]byte(json))
	if err != nil {
		return nil, fmt.Errorf("serviceaccount is invalid: %s", err)
	}
	this.client = oauth2.NewClient(this.ctx, this.credentials.TokenSource)
	//this.client=cfg.Client(ctx)

	this.service, err = googledns.New(this.client)
	if err != nil {
		return nil, err
	}

	return this, nil
}

func (h *Handler) ProviderType() string {
	return TYPE_CODE
}

func (this *Handler) GetZones() (provider.DNSHostedZones, error) {
	rt := provider.M_LISTZONES
	raw := []*googledns.ManagedZone{}
	f := func(resp *googledns.ManagedZonesListResponse) error {
		for _, zone := range resp.ManagedZones {
			raw = append(raw, zone)
		}
		this.metrics.AddRequests(rt, 1)
		rt = provider.M_PLISTZONES
		return nil
	}

	if err := this.service.ManagedZones.List(this.credentials.ProjectID).Pages(this.ctx, f); err != nil {
		return nil, err
	}

	zones := provider.DNSHostedZones{}
	for _, z := range raw {
		forwarded := []string{}
		f := func(r *googledns.ResourceRecordSet) {
			if r.Type == dns.RS_NS {
				if r.Name != z.DnsName {
					forwarded = append(forwarded, dns.NormalizeHostname(r.Name))
				}
			}
		}
		this.handleRecordSets(z.Name, f)
		hostedZone := provider.NewDNSHostedZone(this.ProviderType(),
			z.Name, dns.NormalizeHostname(z.DnsName), "", forwarded)
		zones = append(zones, hostedZone)
	}

	return zones, nil
}

func (this *Handler) handleRecordSets(zoneid string, f func(r *googledns.ResourceRecordSet)) error {
	rt := provider.M_LISTRECORDS
	aggr := func(resp *googledns.ResourceRecordSetsListResponse) error {
		for _, r := range resp.Rrsets {
			f(r)
		}
		this.metrics.AddRequests(rt, 1)
		rt = provider.M_PLISTRECORDS
		return nil
	}
	return this.service.ResourceRecordSets.List(this.credentials.ProjectID, zoneid).Pages(this.ctx, aggr)
}

func (this *Handler) GetZoneState(zone provider.DNSHostedZone) (provider.DNSZoneState, error) {
	dnssets := dns.DNSSets{}

	f := func(r *googledns.ResourceRecordSet) {
		if dns.SupportedRecordType(r.Type) {
			rs := dns.NewRecordSet(r.Type, r.Ttl, nil)
			for _, rr := range r.Rrdatas {
				rs.Add(&dns.Record{Value: rr})
			}
			dnssets.AddRecordSetFromProvider(r.Name, rs)
		}
	}

	if err := this.handleRecordSets(zone.Id(), f); err != nil {
		return nil, err
	}

	return provider.NewDNSZoneState(dnssets), nil
}

func (this *Handler) ExecuteRequests(logger logger.LogContext, zone provider.DNSHostedZone, state provider.DNSZoneState, reqs []*provider.ChangeRequest) error {

	exec := NewExecution(logger, this, zone)
	for _, r := range reqs {
		exec.addChange(r)
	}
	if this.config.DryRun {
		logger.Infof("no changes in dryrun mode for AWS")
		return nil
	}
	return exec.submitChanges(this.metrics)
}
