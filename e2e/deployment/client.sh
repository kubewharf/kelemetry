#!/usr/bin/env bash

kubectl create deployment demo --image=alpine:3.16 --replicas=2 -- sleep infinity
sleep 5
kubectl scale deployment demo --replicas=4
sleep 5
kubectl set image deployments demo alpine=alpine:3.17
sleep 5
kubectl scale deployment demo --replicas=2
sleep 5
kubectl delete deployment demo
sleep 30
