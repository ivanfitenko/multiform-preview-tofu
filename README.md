this is an initial attempt to make terraform reconcile all the resources in its
subdirectories

build it with "make build" or whatever else your LLM suggests.

Try it for yourself:
- cd to directory try_me/
- cd to sample1/ then run terraform init && terraform apply. A random number will be generated
- cd to sample2/ then run terraform init && terraform apply. A random number from the previous step will be modified
- cd back to sample1/ and then run terraform init && terraform apply. Guess what happens to outputs in sample2? Nothing. And that could be your Loadbalancer or RDS IPs

- now cd to root try_me/ directory, and run then terraform init && terraform apply from there. See the trick? Everything gets reconciliated together!
